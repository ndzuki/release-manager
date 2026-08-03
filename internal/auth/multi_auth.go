package auth

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
)

// TryAllInterceptor tries each sub-interceptor in order until one succeeds
// (returns nil error). Only auth errors (Unauthenticated, PermissionDenied)
// trigger fallthrough to the next interceptor; other errors propagate immediately.
func TryAllInterceptor(_ *slog.Logger, interceptors ...connect.UnaryInterceptorFunc) connect.UnaryInterceptorFunc {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		final := next
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			for _, ic := range interceptors {
				called := false
				var lastErr error
				wrapped := ic(func(ctx2 context.Context, req2 connect.AnyRequest) (connect.AnyResponse, error) {
					called = true
					return final(ctx2, req2)
				})
				response, err := wrapped(ctx, req)
				if err == nil {
					return response, nil
				}
				lastErr = err
				code := connect.CodeOf(err)
				if code != connect.CodeUnauthenticated && code != connect.CodePermissionDenied {
					return nil, err
				}
				if !called {
					return nil, lastErr
				}
			}
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no valid authentication method"))
		}
	})
}
