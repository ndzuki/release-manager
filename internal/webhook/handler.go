// Package webhook handles Harbor webhook HTTP requests.
//
// Receives Harbor PUSH_HELMCHART events, validates HMAC signatures,
// and triggers release notification flows.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
)

// HarborWebhookPayload is the Harbor webhook PUSH_HELMCHART event payload.
// Reference: https://goharbor.io/docs/latest/working-with-projects/project-configuration/configure-webhooks/
type HarborWebhookPayload struct {
	Type      string     `json:"type"`
	OccurAt   int64      `json:"occur_at"`
	Operator  string     `json:"operator"`
	EventData *EventData `json:"event_data"`
}

// EventData contains the webhook event details.
type EventData struct {
	Resources  []HarborResource `json:"resources"`
	Repository HarborRepository `json:"repository"`
}

// HarborResource represents a pushed Helm chart resource.
type HarborResource struct {
	Digest      string `json:"digest"`
	Tag         string `json:"tag"`
	ResourceURL string `json:"resource_url"`
}

// HarborRepository represents Harbor repository metadata.
type HarborRepository struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	RepoFullName string `json:"repo_full_name"`
	RepoType     string `json:"repo_type"`
}

// ReleaseNotification is extracted from the Harbor webhook payload.
type ReleaseNotification struct {
	RequestID    string    `json:"request_id"`
	ChartName    string    `json:"chart_name"`
	ChartVersion string    `json:"chart_version"`
	RepoFullName string    `json:"repo_full_name"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// NotifierFunc is called when a release notification is successfully received.
type NotifierFunc func(ReleaseNotification) error

// maxBodySize limits request body size to 1 MiB.
const maxBodySize = 1 << 20

// Handler processes Harbor webhook HTTP requests.
type Handler struct {
	log      logr.Logger
	hmacKey  string
	notifier NotifierFunc
}

// NewHandler creates a new webhook Handler.
// hmacKey is empty to skip HMAC signature verification.
func NewHandler(log logr.Logger, hmacKey string, notifier NotifierFunc) *Handler {
	return &Handler{log: log, hmacKey: hmacKey, notifier: notifier}
}

// ServeHTTP handles Harbor webhook HTTP POST requests.
//
// Flow:
//  1. Validate Content-Type and Method
//  2. Limit request body (1 MiB)
//  3. Verify HMAC-SHA256 signature (if configured)
//  4. Parse JSON payload
//  5. Only process PUSH_HELMCHART events, call notifier callback
//
// @Summary      Receive Harbor webhook
// @Description  Process Harbor PUSH_HELMCHART events, verify HMAC signature
// @Tags         Webhook
// @Accept       json
// @Produce      json
// @Param        request  body      HarborWebhookPayload  true  "Harbor webhook payload"
// @Success      200      {object}  map[string]string      "Success"
// @Failure      400      {object}  map[string]string      "Invalid request"
// @Failure      500      {object}  map[string]string      "Processing failed"
// @Security     HmacSignature
// @Router       /api/v1/webhook/harbor [post]
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Content-Type must be application/json"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large or unreadable"})
		return
	}

	if err := h.verifySignature(r, body); err != nil {
		h.log.Error(err, "HMAC verification failed")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "signature verification failed"})
		return
	}

	var payload HarborWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if payload.Type != "PUSH_HELMCHART" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": fmt.Sprintf("event type %q is not PUSH_HELMCHART", payload.Type)})
		return
	}

	if payload.EventData == nil || len(payload.EventData.Resources) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "no resources in event data"})
		return
	}

	res := payload.EventData.Resources[0]
	notification := ReleaseNotification{
		RequestID:    GenerateRequestID(),
		ChartName:    payload.EventData.Repository.Name,
		ChartVersion: res.Tag,
		RepoFullName: payload.EventData.Repository.RepoFullName,
		OccurredAt:   time.Unix(payload.OccurAt, 0),
	}

	if err := h.notifier(notification); err != nil {
		h.log.Error(err, "notification callback failed", "chart", notification.ChartName)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal processing failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "request_id": notification.RequestID})
}

// verifySignature validates the Harbor webhook HMAC-SHA256 signature.
// Harbor sends the signature in the Authorization header:
//
//	Authorization: Harbor-Signature <base64-encoded-hmac-sha256>
//
// Uses hmac.Equal for constant-time comparison to prevent timing attacks.
func (h *Handler) verifySignature(r *http.Request, body []byte) error {
	if h.hmacKey == "" {
		return nil
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return fmt.Errorf("missing Authorization header")
	}

	const prefix = "Harbor-Signature "
	if !strings.HasPrefix(authHeader, prefix) {
		return fmt.Errorf("invalid Authorization header format")
	}

	sigB64 := strings.TrimPrefix(authHeader, prefix)
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("invalid base64 signature: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(h.hmacKey))
	mac.Write(body)
	expected := mac.Sum(nil)

	if !hmac.Equal(sig, expected) {
		return fmt.Errorf("HMAC signature mismatch")
	}

	return nil
}

// GenerateRequestID generates a unique request tracking ID (UUID v4).
func GenerateRequestID() string {
	return uuid.New().String()
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data) //nolint:errcheck // best-effort: response already committed
}
