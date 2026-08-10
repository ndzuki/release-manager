package interceptor

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/ndzuki/release-manager/internal/contracts"
)

// NewErrorSanitizeInterceptor returns a Connect interceptor that sanitizes
// handler errors at the service boundary (AC-010-04):
//   - CodeInternal errors are reduced to a generic "internal error" message;
//     the full detail (SQL, stack traces, credentials) is logged server-side
//     with the request_id and never reaches the client.
//   - non-connect errors are mapped to CodeInternal with the generic message.
//   - business errors (any other code) keep their code, message, and details.
//   - request_id from ctx is attached as error metadata when missing.
//
// It must be wrapped by NewRequestIDInterceptor so ctx carries the request_id.
func NewErrorSanitizeInterceptor(logger *slog.Logger) connect.Interceptor {
	return errorSanitizeInterceptor{logger: logger}
}

type errorSanitizeInterceptor struct {
	logger *slog.Logger
}

func (i errorSanitizeInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		if err == nil {
			return resp, nil
		}
		return resp, i.sanitize(ctx, req.Spec().Procedure, err)
	}
}

func (i errorSanitizeInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i errorSanitizeInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := next(ctx, conn); err != nil {
			return i.sanitize(ctx, conn.Spec().Procedure, err)
		}
		return nil
	}
}

func (i errorSanitizeInterceptor) sanitize(ctx context.Context, procedure string, err error) error {
	rid := contracts.RequestID(ctx)

	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		if connectErr.Code() != connect.CodeInternal {
			// Business error: stable code + message, keep structured details.
			if rid != "" {
				connectErr.Meta().Set(contracts.RequestIDHeader, rid)
			}
			return connectErr
		}
		// Internal error: log full detail, return the generic message.
		i.logDetail(procedure, rid, err)
		return genericInternalError(rid)
	}

	i.logDetail(procedure, rid, err)
	return genericInternalError(rid)
}

func (i errorSanitizeInterceptor) logDetail(procedure, rid string, err error) {
	if i.logger == nil {
		return
	}
	i.logger.Error("error sanitized at boundary",
		"request_id", rid, "procedure", procedure, "detail", err)
}

func genericInternalError(rid string) *connect.Error {
	sanitized := connect.NewError(connect.CodeInternal, errors.New("internal error"))
	if rid != "" {
		sanitized.Meta().Set(contracts.RequestIDHeader, rid)
	}
	return sanitized
}
