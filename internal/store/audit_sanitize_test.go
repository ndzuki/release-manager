package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSanitizeAuditEventRedactsEveryTextSurface is the TASK-097 gate on the
// transactional write path: a direct audit insert must never store a secret in
// ChangeSummary or in any Metadata value.
func TestSanitizeAuditEventRedactsEveryTextSurface(t *testing.T) {
	event := &AuditEvent{
		ID:             "event-1",
		OrganizationID: "org-1",
		Action:         "operator.revoked",
		ChangeSummary:  `revoked with password=hunter2`,
		Metadata: map[string]string{
			"reason":      "because token=abc123",
			"api_key":     "sk-live-1234567890",
			"certificate": "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----",
			"safe":        "operator one",
		},
	}

	sanitized := SanitizeAuditEvent(event)

	assert.NotContains(t, sanitized.ChangeSummary, "hunter2")
	assert.NotContains(t, sanitized.Metadata["reason"], "abc123")
	assert.NotContains(t, sanitized.Metadata["api_key"], "sk-live-1234567890")
	// The field-name rule replaces the PEM header (the redact package's audited
	// vocabulary); scrubbing a PEM body is outside this card's contract.
	assert.Contains(t, sanitized.Metadata["certificate"], "REDACTED")
	assert.NotContains(t, sanitized.Metadata["certificate"], "-----BEGIN CERTIFICATE-----")
	assert.Equal(t, "operator one", sanitized.Metadata["safe"])

	// The input event is not mutated: callers keep whatever they passed.
	assert.Contains(t, event.ChangeSummary, "hunter2")
	assert.Contains(t, event.Metadata["reason"], "abc123")

	t.Run("nil is tolerated", func(t *testing.T) {
		assert.Nil(t, SanitizeAuditEvent(nil))
	})

	t.Run("nil metadata stays nil", func(t *testing.T) {
		assert.Nil(t, SanitizeAuditEvent(&AuditEvent{ID: "e"}).Metadata)
	})
}
