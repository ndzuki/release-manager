package audit

import (
	"github.com/ndzuki/release-manager/internal/redact"
	"github.com/ndzuki/release-manager/internal/store"
)

// Sanitize redacts sensitive values from a string payload.
// Returns the sanitized string and a boolean indicating whether any
// content was redacted. Re-exported from the dependency-free redact
// package so existing callers keep a stable import path.
func Sanitize(payload string) (string, bool) {
	return redact.Sanitize(payload)
}

// IsSensitiveField returns true if the field name indicates sensitive content.
func IsSensitiveField(fieldName string) bool {
	return redact.IsSensitiveField(fieldName)
}

// RedactSensitive redacts a field value if the field name is sensitive.
// Returns the (possibly redacted) value.
func RedactSensitive(fieldName, value string) string {
	return redact.Sensitive(fieldName, value)
}

// sanitizeAuditEvent returns a copy of ev with ChangeSummary and Metadata values
// redacted for export.
func sanitizeAuditEvent(ev *store.AuditEvent) *store.AuditEvent {
	if ev == nil {
		return nil
	}
	out := *ev
	out.ChangeSummary, _ = Sanitize(ev.ChangeSummary)
	if ev.Metadata != nil {
		out.Metadata = make(map[string]string, len(ev.Metadata))
		for k, v := range ev.Metadata {
			out.Metadata[k] = RedactSensitive(k, v)
		}
	}
	return &out
}
