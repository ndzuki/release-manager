// Package interceptor provides Connect interceptors for shared API contracts:
// request_id injection and idempotency enforcement.
package interceptor

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/contracts"
)

// NewRequestIDInterceptor returns a Connect unary interceptor that ensures every
// request carries a request_id. It reads X-Request-Id from the request header;
// if missing, it generates a new UUIDv4. The request_id is injected into the
// context and set on the response trailer for end-to-end traceability.
func NewRequestIDInterceptor(logger *slog.Logger) connect.UnaryInterceptorFunc {
	interceptor := func(next connect.UnaryFunc) connect.UnaryFunc {
		return connect.UnaryFunc(func(
			ctx context.Context,
			req connect.AnyRequest,
		) (connect.AnyResponse, error) {
			requestID := req.Header().Get("X-Request-Id")
			if requestID == "" {
				requestID = uuid.New().String()
			}

			ctx = context.WithValue(ctx, contracts.RequestIDKey, requestID)
			req.Header().Set("X-Request-Id", requestID)

			resp, err := next(ctx, req)

			// Echo request_id back on the response header for client traceability.
			if resp != nil {
				resp.Header().Set("X-Request-Id", requestID)
			}

			if err != nil && logger != nil {
				logger.Error("request failed",
					"request_id", requestID,
					"procedure", req.Spec().Procedure,
					"error", err,
				)
			}

			return resp, err
		})
	}
	return connect.UnaryInterceptorFunc(interceptor)
}
