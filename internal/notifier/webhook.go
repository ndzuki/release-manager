package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// Error codes for notification delivery outcomes.
const (
	ErrCodeDelivered        = "delivered"
	ErrCodeRateLimited      = "rate_limited"
	ErrCodeNetworkError     = "network_error"
	ErrCodeInvalidRecipient = "invalid_recipient"
	ErrCodeCredentialInvalid = "credential_invalid"
	ErrCodeTimeout           = "timeout"
	ErrCodeInternal          = "internal"
)

// webhookSender delivers notifications via HTTP POST (JSON).
type webhookSender struct {
	client    *http.Client
	resolver  SecretResolver
	secretKey string // key passed to SecretResolver for the webhook secret
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

// NewWebhookSender creates a sender that POSTs JSON to the job recipient URL.
func NewWebhookSender(client *http.Client, opts ...WebhookSenderOption) Sender {
	s := &webhookSender{
		client: client,
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
func (s *webhookSender) Send(ctx context.Context, job *store.NotificationJob) (string, bool, error) {
	if job.Channel != store.NotificationChannelWebhook {
		return ErrCodeInvalidRecipient, true,
			fmt.Errorf("channel %s not supported by webhook sender", job.Channel)
	}

	payload := webhookPayload{
		OperationID: job.OperationID,
		Channel:     string(job.Channel),
		Recipient:   job.Recipient,
		Metadata:    job.Metadata,
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

	// Resolve and inject webhook secret if configured.
	if s.resolver != nil && s.secretKey != "" {
		secret, err := s.resolver.Resolve(ctx, s.secretKey)
		if err != nil {
			return ErrCodeCredentialInvalid, true,
				fmt.Errorf("resolve webhook secret: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return classifyNetworkError(err)
	}
	defer resp.Body.Close()

	// Drain body to reuse connection.
	_, _ = io.Copy(io.Discard, resp.Body)

	return classifyHTTPStatus(resp.StatusCode)
}

// classifyHTTPStatus maps HTTP status codes to stable error codes.
func classifyHTTPStatus(code int) (string, bool, error) {
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
		return ErrCodeCredentialInvalid, true,
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
func classifyNetworkError(err error) (string, bool, error) {
	errStr := err.Error()
	switch {
	case strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "context deadline exceeded"):
		return ErrCodeTimeout, false, fmt.Errorf("webhook timeout: %w", err)
	default:
		return ErrCodeNetworkError, false, fmt.Errorf("webhook network error: %w", err)
	}
}
