// Package interceptor provides Connect interceptors for shared API contracts.
package interceptor

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/contracts"
)

const requestIDHeader = contracts.RequestIDHeader

// NewRequestIDInterceptor propagates a request identifier for unary and streaming RPCs.
func NewRequestIDInterceptor(logger *slog.Logger) connect.Interceptor {
	return requestIDInterceptor{logger: logger}
}

type requestIDInterceptor struct {
	logger *slog.Logger
}

func (i requestIDInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		requestID := requestIDFromHeader(req.Header().Get(requestIDHeader))
		ctx = contracts.WithRequestID(ctx, requestID)

		response, err := next(ctx, req)
		if response != nil {
			response.Header().Set(requestIDHeader, requestID)
		}
		if err != nil {
			var connectErr *connect.Error
			if errors.As(err, &connectErr) {
				connectErr.Meta().Set(requestIDHeader, requestID)
			}
			i.logFailure(req.Spec().Procedure, requestID, err)
		}
		return response, err
	}
}

func (i requestIDInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i requestIDInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		requestID := requestIDFromHeader(conn.RequestHeader().Get(requestIDHeader))
		ctx = contracts.WithRequestID(ctx, requestID)
		conn.ResponseHeader().Set(requestIDHeader, requestID)
		err := next(ctx, conn)
		if err != nil {
			var connectErr *connect.Error
			if errors.As(err, &connectErr) {
				connectErr.Meta().Set(requestIDHeader, requestID)
			}
			i.logFailure(conn.Spec().Procedure, requestID, err)
		}
		return err
	}
}

func (i requestIDInterceptor) logFailure(procedure, requestID string, err error) {
	if i.logger == nil {
		return
	}
	i.logger.Error("request failed", "request_id", requestID, "procedure", procedure, "error", err)
}

func requestIDFromHeader(requestID string) string {
	if requestID != "" {
		return requestID
	}
	return uuid.NewString()
}
