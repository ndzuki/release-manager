// Package contracts provides shared cross-cutting contracts for the release-manager:
// request tracing, idempotency, error sanitization, and cursor pagination.
package contracts

import "context"

// Context keys for request metadata propagation.
type (
	requestIDKey   struct{}
	operationIDKey struct{}
)

// RequestIDKey is kept for compatibility with existing in-process callers.
var RequestIDKey requestIDKey

// OperationIDKey is kept for compatibility with existing in-process callers.
var OperationIDKey operationIDKey

// WithRequestID returns a child context carrying the request identifier.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, RequestIDKey, requestID)
}

// RequestIDFromContext extracts the request identifier, or returns an empty string.
func RequestIDFromContext(ctx context.Context) string {
	if requestID, ok := ctx.Value(RequestIDKey).(string); ok {
		return requestID
	}
	return ""
}

// WithOperationID returns a child context carrying the publish-chain operation identifier.
func WithOperationID(ctx context.Context, operationID string) context.Context {
	return context.WithValue(ctx, OperationIDKey, operationID)
}

// OperationIDFromContext extracts the operation identifier, or returns an empty string.
func OperationIDFromContext(ctx context.Context) string {
	if operationID, ok := ctx.Value(OperationIDKey).(string); ok {
		return operationID
	}
	return ""
}

// RequestID is kept as the concise accessor used by existing callers.
func RequestID(ctx context.Context) string { return RequestIDFromContext(ctx) }

// OperationID is kept as the concise accessor used by existing callers.
func OperationID(ctx context.Context) string { return OperationIDFromContext(ctx) }
