package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// NewAuthInterceptor creates a Connect interceptor that authenticates JWTs and enforces RBAC.
func NewAuthInterceptor(
	jwt *JWTManager,
	enforcer *Enforcer,
	publicMethods map[string]bool,
	logger *slog.Logger,
) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			procedure := req.Spec().Procedure
			if publicMethods[procedure] {
				return next(ctx, req)
			}

			token := extractToken(req.Header().Get("Authorization"))
			if token == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing authorization header"))
			}

			claims, err := jwt.ValidateAccessToken(token)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid token: %w", err))
			}

			domain, err := resolveDomain(req.Any(), claims.OrgID)
			if err != nil {
				return nil, authorizationConnectError(err, enforcer.PolicyVersion())
			}
			object, action := mapProcedure(procedure)
			if object == "" || action == "" {
				return nil, authorizationConnectError(newInvalidActorContext(
					claims.UserID,
					domain,
					fmt.Errorf("unmapped procedure %q", procedure),
				), enforcer.PolicyVersion())
			}

			if err := enforceRequestBinding(ctx, enforcer, req.Any(), procedure, domain); err != nil {
				logger.Warn(
					"access denied",
					"user_id", claims.UserID,
					"organization_id", domain,
					"procedure", procedure,
					"reason_code", authorizationReason(err),
				)
				return nil, authorizationConnectError(err, enforcer.PolicyVersion())
			}
			if err := enforcer.Enforce(claims.UserID, domain, object, action); err != nil {
				logger.Warn(
					"access denied",
					"user_id", claims.UserID,
					"organization_id", domain,
					"procedure", procedure,
					"reason_code", authorizationReason(err),
				)
				return nil, authorizationConnectError(err, enforcer.PolicyVersion())
			}

			ctx = context.WithValue(ctx, userIDKey, claims.UserID)
			ctx = context.WithValue(ctx, rolesKey, claims.Roles)
			ctx = context.WithValue(ctx, orgIDKey, domain)
			return next(ctx, req)
		}
	}
}

func extractToken(authHeader string) string {
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(authHeader, "Bearer ")
}

func resolveDomain(request any, tokenOrgID string) (string, error) {
	requestOrgID := protoStringField(request, "org_id")
	if requestOrgID != "" {
		if tokenOrgID != "" && tokenOrgID != requestOrgID {
			return "", newPermissionDenied("", requestOrgID, "organization", "access")
		}
		return requestOrgID, nil
	}
	if tokenOrgID == "" {
		return "", newInvalidActorContext("", "", errors.New("organization is required"))
	}
	return tokenOrgID, nil
}

func enforceRequestBinding(
	ctx context.Context,
	enforcer *Enforcer,
	request any,
	procedure string,
	domain string,
) error {
	if strings.Contains(procedure, "BindingService") && strings.HasSuffix(procedure, "/CreateBinding") {
		return nil
	}
	if customerID := protoStringField(request, "customer_id"); customerID != "" {
		return enforcer.CheckBinding(ctx, domain, customerID)
	}
	if bindingID := protoStringField(request, "binding_id"); bindingID != "" {
		return enforcer.CheckBindingID(ctx, domain, bindingID)
	}
	return nil
}

// mapProcedure maps a Connect RPC procedure to a Casbin object and action.
func mapProcedure(procedure string) (object, action string) {
	parts := strings.Split(strings.TrimPrefix(procedure, "/"), "/")
	if len(parts) != 2 {
		return "", ""
	}

	serviceName := parts[0]
	if dot := strings.LastIndex(serviceName, "."); dot >= 0 {
		serviceName = serviceName[dot+1:]
	}
	return mapServiceToObject(serviceName), mapMethodToAction(parts[1])
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
		return ""
	}
}

func mapMethodToAction(method string) string {
	switch {
	case strings.HasPrefix(method, "List"), strings.HasPrefix(method, "Get"),
		strings.HasPrefix(method, "Validate"):
		return "read"
	case strings.HasPrefix(method, "Create"), strings.HasPrefix(method, "Add"),
		strings.HasPrefix(method, "Update"), strings.HasPrefix(method, "Disable"),
		strings.HasPrefix(method, "Remove"), strings.HasPrefix(method, "Revoke"),
		strings.HasPrefix(method, "Delete"), strings.HasPrefix(method, "Change"):
		return "write"
	default:
		return ""
	}
}

func protoStringField(request any, name protoreflect.Name) string {
	message, ok := request.(interface{ ProtoReflect() protoreflect.Message })
	if !ok || message == nil {
		return ""
	}
	reflected := message.ProtoReflect()
	field := reflected.Descriptor().Fields().ByName(name)
	if field == nil || field.Kind() != protoreflect.StringKind || !reflected.Has(field) {
		return ""
	}
	return reflected.Get(field).String()
}

func authorizationConnectError(err error, policyVersion uint64) error {
	connectErr := connect.NewError(connect.CodePermissionDenied, err)
	var unavailable *PolicyUnavailableError
	if errors.As(err, &unavailable) {
		connectErr = connect.NewError(connect.CodeUnavailable, err)
	}
	connectErr.Meta().Set("X-Reason-Code", authorizationReason(err))
	connectErr.Meta().Set("X-Policy-Version", strconv.FormatUint(policyVersion, 10))
	return connectErr
}

func authorizationReason(err error) string {
	var reasoner interface{ AuthorizationReason() string }
	if errors.As(err, &reasoner) {
		return reasoner.AuthorizationReason()
	}
	return "permission_denied"
}

type (
	rolesCtxKey string
	orgIDCtxKey string
)

const (
	rolesKey rolesCtxKey = "roles"
	orgIDKey orgIDCtxKey = "orgID"
)
