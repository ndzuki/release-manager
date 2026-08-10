package contracts

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
)

// RequestIDHeader is the HTTP header carrying the request identifier across
// the wire. Response errors attach it as metadata so clients can correlate
// failures with server-side logs (AC-010-04).
const RequestIDHeader = "X-Request-ID"

// NewAppError creates a connect.Error with the given code and message.
// The message MUST be a stable, client-safe description — internal details
// (stack traces, SQL, credentials) MUST NOT leak into msg. The request_id
// from ctx, when present, is attached as error metadata for traceability
// instead of being spliced into the message.
func NewAppError(ctx context.Context, code connect.Code, msg string) *connect.Error {
	err := connect.NewError(code, fmt.Errorf("%s", msg))
	injectRequestID(ctx, err)
	return err
}

// NewAppErrorf is like NewAppError but accepts a format string and args.
func NewAppErrorf(ctx context.Context, code connect.Code, format string, args ...interface{}) *connect.Error {
	return NewAppError(ctx, code, fmt.Sprintf(format, args...))
}

// ToConnectError sanitizes an arbitrary error into a connect.Error suitable
// for client consumption (AC-010-04):
//   - a *connect.Error is preserved: code, message, and structured details
//     (e.g. FieldViolation) are kept; request_id is attached as metadata.
//   - any other error is treated as an internal failure and mapped to
//     CodeInternal with a generic message. Full detail is for server logs only.
func ToConnectError(ctx context.Context, err error) *connect.Error {
	if err == nil {
		return nil
	}

	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		injectRequestID(ctx, connectErr)
		return connectErr
	}

	sanitized := connect.NewError(connect.CodeInternal, errors.New("internal error"))
	injectRequestID(ctx, sanitized)
	return sanitized
}

func injectRequestID(ctx context.Context, err *connect.Error) {
	if rid := RequestID(ctx); rid != "" {
		err.Meta().Set(RequestIDHeader, rid)
	}
}
