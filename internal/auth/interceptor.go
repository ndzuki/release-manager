package auth

import (
	"context"
	"fmt"
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

			// Extract token from Authorization header.
			token := extractToken(req.Header().Get("Authorization"))
			if token == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated,
					fmt.Errorf("missing authorization header"))
			}

			claims, err := jwt.ValidateAccessToken(token)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated,
					fmt.Errorf("invalid token: %w", err))
			}

			ctx = context.WithValue(ctx, userIDKey, claims.UserID)
			ctx = context.WithValue(ctx, rolesKey, claims.Roles)
			ctx = context.WithValue(ctx, organizationIDKey, claims.OrgID)

			// REQ-027: Enforce RBAC.
			// Map procedure to (domain, obj, act).
			dom, obj, act := mapProcedure(procedure, claims.OrgID)
			if err := enforcer.Enforce(claims.UserID, dom, obj, act); err != nil {
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
// Procedures follow the pattern: /package.Service/Method.
func mapProcedure(procedure, domain string) (mappedDomain, object, action string) {
	parts := strings.Split(strings.TrimPrefix(procedure, "/"), "/")
	if len(parts) < 2 {
		return domain, "*", "*"
	}

	service := parts[0]
	method := parts[1]
	obj := mapServiceToObject(service)
	act := mapMethodToAction(method)
	return domain, obj, act
}

func mapServiceToObject(service string) string {
	switch {
	case strings.Contains(service, "Organization"):
		return "organization"
	case strings.Contains(service, "Binding"):
		return "binding"
	case strings.Contains(service, "Auth"):
		return "auth"
	case strings.Contains(service, "Orchestrator"):
		return "release"
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
		strings.HasPrefix(method, "Remove"), strings.HasPrefix(method, "Revoke"),
		strings.HasPrefix(method, "Delete"), strings.HasPrefix(method, "Emergency"),
		strings.HasPrefix(method, "Publish"), strings.HasPrefix(method, "Configure"),
		strings.HasPrefix(method, "Sync"):
		return "write"
	default:
		return "*"
	}
}

type rolesCtxKey string

const rolesKey rolesCtxKey = "roles"

type organizationCtxKey string

const organizationIDKey organizationCtxKey = "organizationID"

// UserIDFromContext returns the authenticated user ID.
func UserIDFromContext(ctx context.Context) string {
	userID, ok := ctx.Value(userIDKey).(string)
	if !ok {
		return ""
	}
	return userID
}

// OrganizationIDFromContext returns the authenticated organization ID.
func OrganizationIDFromContext(ctx context.Context) string {
	organizationID, ok := ctx.Value(organizationIDKey).(string)
	if !ok {
		return ""
	}
	return organizationID
}
