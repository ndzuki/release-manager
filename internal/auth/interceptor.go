package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
)

// NewAuthInterceptor creates a Connect interceptor that:
// 1. Extracts and validates the JWT access token from Authorization header
// 2. Injects user ID into context
// 3. Enforces Casbin RBAC for protected procedures
func NewAuthInterceptor(jwt *JWTManager, enforcer *Enforcer, publicMethods map[string]bool, logger *slog.Logger) connect.UnaryInterceptorFunc {
	interceptor := func(next connect.UnaryFunc) connect.UnaryFunc {
		return connect.UnaryFunc(func(
			ctx context.Context,
			req connect.AnyRequest,
		) (connect.AnyResponse, error) {
			procedure := req.Spec().Procedure

			// Allow public methods (Login, etc.) through without auth.
			if publicMethods[procedure] {
				return next(ctx, req)
			}

			// Browser requests authenticate with the HttpOnly access cookie.
			token := extractToken(req.Header().Get("Authorization"))
			if token == "" {
				token = cookieValue(req.Header(), accessCookieName)
			}
			if token == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing access session"))
			}

			claims, err := jwt.ValidateAccessToken(token)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid access session"))
			}

			ctx = context.WithValue(ctx, userIDKey, claims.UserID)
			ctx = context.WithValue(ctx, rolesKey, claims.Roles)
			ctx = context.WithValue(ctx, orgIDKey, claims.OrgID)
			// REQ-027: Enforce RBAC with the server-issued organization domain.
			object, action := mapProcedure(procedure)
			domain := claims.OrgID
			if domain == "" {
				domain = "*"
			}
			if err := enforcer.Enforce(claims.UserID, domain, object, action); err != nil {
				logger.Warn("access denied",
					"user_id", claims.UserID,
					"procedure", procedure,
					"reason", err.Error(),
				)
				return nil, connect.NewError(connect.CodePermissionDenied, err)
			}

			return next(ctx, req)
		})
	}
	return connect.UnaryInterceptorFunc(interceptor)
}

func extractToken(authHeader string) string {
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(authHeader, "Bearer ")
}

// mapProcedure maps a Connect RPC procedure to Casbin (domain, obj, act).
// Procedures follow the pattern: /package.Service/Method
func mapProcedure(procedure string) (object, action string) {
	parts := strings.Split(strings.TrimPrefix(procedure, "/"), "/")
	if len(parts) < 2 {
		return "*", "*"
	}

	service := parts[0]
	method := parts[1]

	return mapServiceToObject(service), mapMethodToAction(method)
}

func mapServiceToObject(service string) string {
	switch {
	case strings.Contains(service, "Organization"):
		return "organization"
	case strings.Contains(service, "Binding"):
		return "binding"
	case strings.Contains(service, "Auth"):
		return "auth"
	default:
		return "*"
	}
}

func mapMethodToAction(method string) string {
	switch {
	case strings.HasPrefix(method, "List"), strings.HasPrefix(method, "Get"):
		return "read"
	case strings.HasPrefix(method, "Create"), strings.HasPrefix(method, "Add"),
		strings.HasPrefix(method, "Update"), strings.HasPrefix(method, "Disable"),
		strings.HasPrefix(method, "Remove"), strings.HasPrefix(method, "Revoke"):
		return "write"
	case strings.HasPrefix(method, "Delete"):
		return "write"
	default:
		return "*"
	}
}

type rolesCtxKey string

const rolesKey rolesCtxKey = "roles"

type orgIDCtxKey string

const orgIDKey orgIDCtxKey = "orgID"
