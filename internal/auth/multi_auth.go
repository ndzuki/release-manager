package auth

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
)

// TryAllInterceptor tries each sub-interceptor in order until one succeeds
// (returns nil error). Unauthenticated falls through to the next candidate
// (the credential did not match this leg's method); PermissionDenied and any
// other error propagate immediately (the credential WAS authenticated, so a
// later leg must not re-judge or mask it). Auth interceptors reject before
// calling next, so fallthrough applies whether or not the candidate invoked
// its inner chain (REQ-011 §562: `TryAllInterceptor(JWT, ServiceTokenInterceptor)`
// 顺序尝试，二者皆失败返回 unauthenticated — real smoke 2026-08-24: the
// pre-fix short-circuit returned the JWT leg's unauthenticated without ever
// evaluating the service token leg; the same fix must not collapse a JWT
// user's Casbin 403 into 401 either).
func TryAllInterceptor(_ *slog.Logger, interceptors ...connect.UnaryInterceptorFunc) connect.UnaryInterceptorFunc {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		final := next
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			for _, ic := range interceptors {
				wrapped := ic(func(ctx2 context.Context, req2 connect.AnyRequest) (connect.AnyResponse, error) {
					return final(ctx2, req2)
				})
				response, err := wrapped(ctx, req)
				if err == nil {
					return response, nil
				}
				if connect.CodeOf(err) == connect.CodeUnauthenticated {
					continue
				}
				return nil, err
			}
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no valid authentication method"))
		}
	})
}
