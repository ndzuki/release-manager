// Package contracts provides shared cross-cutting contracts for the release-manager:
// request tracing, idempotency, error sanitization, and cursor pagination.
package contracts

import "context"

// Context keys for request metadata propagation.
type (
	requestIDKey   struct{}
	operationIDKey struct{}
)

// RequestIDKey is the context key for the request_id tracing identifier.
var RequestIDKey requestIDKey

// OperationIDKey is the context key for the operation_id publish-chain identifier.
var OperationIDKey operationIDKey

// RequestID extracts the request_id from context, or returns "" if not set.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(RequestIDKey).(string); ok {
		return v
	}
	return ""
}

// OperationID extracts the operation_id from context, or returns "" if not set.
func OperationID(ctx context.Context) string {
	if v, ok := ctx.Value(OperationIDKey).(string); ok {
		return v
	}
	return ""
}
