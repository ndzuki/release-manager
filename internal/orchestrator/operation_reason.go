package orchestrator

import "github.com/ndzuki/release-manager/internal/redact"

// operationReasonDisplayMaxRunes bounds the projected reason. The write path
// already caps an emergency reason at 500 runes (emergencyReasonMaxRunes); this
// is a projection backstop so a row written by any other caller still cannot
// widen the payload.
const operationReasonDisplayMaxRunes = 500

// operationReasonDisplay is the safe display projection of an operation's
// reason (AC-058-48 / TASK-325 W3): redact first, then truncate, so a secret
// smuggled into the free-text reason is never truncated into an unrecognizable
// fragment before the redactor sees it. It returns the empty string for an
// operation that recorded no reason, which the detail page renders as an absent
// row rather than an empty one.
//
// It lives beside toProtoOperation (service.go) because that projection is the
// single choke point every Operation-returning RPC goes through; keeping the
// helper here means the projection call site stays one line, which matters
// because docs/citations.assert pins line numbers in service.go.
func operationReasonDisplay(reason string) string {
	if reason == "" {
		return ""
	}
	sanitized, _ := redact.Sanitize(reason)
	return redact.Truncate(sanitized, operationReasonDisplayMaxRunes)
}
