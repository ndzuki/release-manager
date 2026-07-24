package audit

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	"github.com/ndzuki/release-manager/internal/jwtauth"
)

type principalContextKey struct{}

// Principal is the authenticated identity used by audit authorization.
type Principal struct {
	UserID string
	Roles  []string
	OrgID  string
}

// NewJWTInterceptor validates access tokens and injects the audit principal.
func NewJWTInterceptor(jwt *jwtauth.Manager) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			value := req.Header().Get("Authorization")
			token, ok := strings.CutPrefix(value, "Bearer ")
			if !ok || token == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing authorization header"))
			}

			claims, err := jwt.ValidateAccessToken(token)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid token"))
			}
			ctx = context.WithValue(ctx, principalContextKey{}, Principal{
				UserID: claims.UserID,
				Roles:  claims.Roles,
				OrgID:  claims.OrgID,
			})
			return next(ctx, req)
		}
	}
}

// PrincipalFromContext returns the authenticated audit principal.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}
