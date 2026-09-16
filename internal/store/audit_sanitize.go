package store

import "github.com/ndzuki/release-manager/internal/redact"

// SanitizeAuditEvent returns a copy of ev with ChangeSummary and every Metadata
// value redacted, mirroring internal/audit's sanitize step.
//
// Transactional writers (ADR-009: operator revocation, values approval, bundle
// submission) must insert their audit row inside the state transaction, so they
// cannot go through the asynchronous emitter. They call this instead, which keeps
// the invariant that no path into audit_events can store unsanitized text
// (AGENTS.md hard constraint 6). The redaction vocabulary lives in
// internal/redact, a dependency-free package, so the store layer can apply it
// without importing internal/audit (which imports this package).
func SanitizeAuditEvent(ev *AuditEvent) *AuditEvent {
	if ev == nil {
		return nil
	}
	out := *ev
	out.ChangeSummary, _ = redact.Sanitize(ev.ChangeSummary)
	if ev.Metadata != nil {
		out.Metadata = make(map[string]string, len(ev.Metadata))
		for key, value := range ev.Metadata {
			// Two passes: the field name decides for named credentials, and the
			// content scan catches a secret smuggled into an innocuous field. A
			// transactional backstop must be at least as strict as the
			// asynchronous emitter, so it never relies on the caller naming a
			// field correctly.
			out.Metadata[key], _ = redact.Sanitize(redact.Sensitive(key, value))
		}
	}
	return &out
}
