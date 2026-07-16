package contracts

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
)

// NewAppError creates a connect.Error with the given code and message.
// It automatically injects the request_id from context into the message
// for traceability. Internal details (stack traces, SQL, credentials)
// MUST NOT leak into msg.
func NewAppError(ctx context.Context, code connect.Code, msg string) *connect.Error {
	rid := RequestID(ctx)
	if rid != "" {
		msg = fmt.Sprintf("[%s] %s", rid, msg)
	}
	return connect.NewError(code, fmt.Errorf("%s", msg))
}

// NewAppErrorf is like NewAppError but accepts a format string and args.
func NewAppErrorf(ctx context.Context, code connect.Code, format string, args ...interface{}) *connect.Error {
	return NewAppError(ctx, code, fmt.Sprintf(format, args...))
}

// ToConnectError sanitizes an arbitrary error into a connect.Error suitable
// for client consumption. If err is already a *connect.Error, it preserves
// the code and message but strips internal details and injects request_id.
// Unknown errors are mapped to CodeInternal with a safe message.
func ToConnectError(ctx context.Context, err error) *connect.Error {
	if err == nil {
		return nil
	}

	var connectErr *connect.Error
	if asConnectError(err, &connectErr) {
		return NewAppError(ctx, connectErr.Code(), connectErr.Message())
	}

	return NewAppError(ctx, connect.CodeInternal, "internal error")
}

func asConnectError(err error, target **connect.Error) bool {
	for {
		if ce, ok := err.(*connect.Error); ok {
			*target = ce
			return true
		}
		unwrapped := unwrapOnce(err)
		if unwrapped == nil {
			return false
		}
		err = unwrapped
	}
}

func unwrapOnce(err error) error {
	type wrapper interface {
		Unwrap() error
	}
	if w, ok := err.(wrapper); ok {
		return w.Unwrap()
	}
	return nil
}
