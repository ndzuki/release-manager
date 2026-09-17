package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ndzuki/release-manager/internal/redact"
	"github.com/ndzuki/release-manager/internal/store"
)

// Error codes for notification delivery outcomes.
const (
	ErrCodeDelivered        = "delivered"
	ErrCodeRateLimited      = "rate_limited"
	ErrCodeNetworkError     = "network_error"
	ErrCodeInvalidRecipient = "invalid_recipient"
	ErrCodeCredentialError  = "credential_invalid" //nolint:gosec // stable error code, not a credential
	ErrCodeTimeout          = "timeout"
	ErrCodeInternal         = "internal"
	// ErrCodeEgressBlocked is a non-retryable configuration/authorization
	// outcome: the destination is not on the configured egress allowlist
	// (REQ-031, TASK-096). Retrying cannot help, so it dead-letters.
	ErrCodeEgressBlocked = "egress_blocked"
)

// webhookSender delivers notifications via HTTP POST (JSON).
type webhookSender struct {
	client    *http.Client
	resolver  SecretResolver
	secretKey string // key passed to SecretResolver for the webhook secret
	// policy is the outbound allowlist. A nil policy denies everything: the
	// zero value must be safe, because getting this wrong is an SSRF, not a
	// delivery hiccup.
	policy  *EgressPolicy
	logger  *slog.Logger
	metrics *EgressMetrics
}

// WebhookSenderOption configures the webhook sender.
type WebhookSenderOption func(*webhookSender)

// WithSecretResolution enables secret resolution for webhook auth.
// secretKey is the key passed to SecretResolver.
func WithSecretResolution(resolver SecretResolver, secretKey string) WebhookSenderOption {
	return func(s *webhookSender) {
		s.resolver = resolver
		s.secretKey = secretKey
	}
}

// WithEgressPolicy sets the outbound allowlist (REQ-031/TASK-096). Without it
// the sender denies every destination.
func WithEgressPolicy(policy *EgressPolicy) WebhookSenderOption {
	return func(s *webhookSender) {
		s.policy = policy
	}
}

// WithLogger attaches the structured logger used for denial records.
func WithLogger(logger *slog.Logger) WebhookSenderOption {
	return func(s *webhookSender) {
		if logger != nil {
			s.logger = logger
		}
	}
}

// WithEgressMetrics attaches the admission counters.
func WithEgressMetrics(metrics *EgressMetrics) WebhookSenderOption {
	return func(s *webhookSender) {
		s.metrics = metrics
	}
}

// NewWebhookSender creates a sender that POSTs JSON to the job recipient URL.
func NewWebhookSender(client *http.Client, opts ...WebhookSenderOption) Sender {
	s := &webhookSender{
		client: client,
		logger: slog.Default(),
	}
	if s.client == nil {
		s.client = &http.Client{Timeout: 30 * time.Second}
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

type webhookPayload struct {
	OperationID string            `json:"operation_id"`
	Channel     string            `json:"channel"`
	Recipient   string            `json:"recipient"`
	Metadata    map[string]string `json:"metadata"`
	JobID       string            `json:"job_id"`
}

// Send delivers the notification via HTTP POST with an Idempotency-Key header.
// Returns: error_code, is4xx, error.
func (s *webhookSender) Send(ctx context.Context, job *store.NotificationJob) (
	errorCode string,
	is4xx bool,
	err error,
) {
	if job.Channel != store.NotificationChannelWebhook {
		return ErrCodeInvalidRecipient, true,
			fmt.Errorf("channel %s not supported by webhook sender", job.Channel)
	}

	payload := webhookPayload{
		OperationID: job.OperationID,
		Channel:     string(job.Channel),
		Recipient:   job.Recipient,
		Metadata:    redactMetadata(job.Metadata),
		JobID:       job.ID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ErrCodeInternal, false, fmt.Errorf("marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.Recipient, bytes.NewReader(body))
	if err != nil {
		// Invalid URL → configuration error.
		if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "unsupported protocol") {
			return ErrCodeInvalidRecipient, true, fmt.Errorf("invalid webhook url: %w", err)
		}
		return ErrCodeInternal, false, fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", job.ID)

	// Outbound admission (REQ-031/TASK-096): a destination must be explicitly
	// allowlisted. This runs after the URL is known to be well formed (so a
	// malformed recipient stays a recipient error) and before any credential is
	// resolved or any connection is attempted.
	allowed, reason := s.policy.Allow(job.Recipient)
	if !allowed {
		s.metrics.recordDenied()
		s.logger.Warn("webhook delivery blocked by egress policy",
			"job_id", job.ID,
			"operation_id", job.OperationID,
			"reason", reason,
			"recipient", safeTarget(job.Recipient),
		)
		return ErrCodeEgressBlocked, true, fmt.Errorf("egress blocked: %s", reason)
	}
	s.metrics.recordAllowed()

	// Resolve and inject webhook secret if configured.
	if s.resolver != nil && s.secretKey != "" {
		secret, err := s.resolver.Resolve(ctx, s.secretKey)
		if err != nil {
			return ErrCodeCredentialError, true,
				fmt.Errorf("resolve webhook secret: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		errorCode, classifiedErr := classifyNetworkError(err)
		return errorCode, false, classifiedErr
	}
	defer resp.Body.Close()

	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return ErrCodeNetworkError, false, fmt.Errorf("drain webhook response: %w", err)
	}

	return classifyHTTPStatus(resp.StatusCode)
}

// classifyHTTPStatus maps HTTP status codes to stable error codes.
func classifyHTTPStatus(code int) (errorCode string, is4xx bool, err error) {
	switch {
	case code >= 200 && code < 300:
		return ErrCodeDelivered, false, nil
	case code == 429:
		return ErrCodeRateLimited, false,
			fmt.Errorf("webhook rate limited (HTTP 429)")
	case code >= 500:
		return ErrCodeNetworkError, false,
			fmt.Errorf("webhook server error (HTTP %d)", code)
	case code == 401 || code == 403:
		return ErrCodeCredentialError, true,
			fmt.Errorf("webhook auth failed (HTTP %d)", code)
	case code == 404 || code == 400:
		return ErrCodeInvalidRecipient, true,
			fmt.Errorf("webhook recipient error (HTTP %d)", code)
	default:
		// Other 4xx → configuration error.
		return "4xx_unknown", true,
			fmt.Errorf("webhook client error (HTTP %d)", code)
	}
}

// classifyNetworkError maps transport errors to stable error codes.
func classifyNetworkError(err error) (errorCode string, classifiedErr error) {
	errStr := err.Error()
	switch {
	case strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "context deadline exceeded"):
		return ErrCodeTimeout, fmt.Errorf("webhook timeout: %w", err)
	default:
		return ErrCodeNetworkError, fmt.Errorf("webhook network error: %w", err)
	}
}

// redactMetadata returns a copy of the outbound metadata with every value run
// through the shared redaction path (AGENTS.md hard constraint 6, REQ-031): a
// notification body must not be the place a secret escapes, even when a caller
// forgot to scrub it. The durable job row keeps the original values.
func redactMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		out[key] = redact.Sensitive(key, value)
		if sanitized, changed := redact.Sanitize(out[key]); changed {
			out[key] = sanitized
		}
	}
	return out
}

// safeTarget strips URL userinfo before a destination is logged, so a
// credential embedded in a recipient URL never reaches the log stream.
func safeTarget(target string) string {
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil || parsed.User == nil {
		return target
	}
	parsed.User = nil
	return parsed.String()
}
