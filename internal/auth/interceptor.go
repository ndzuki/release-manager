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

	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// NewAuthInterceptor creates a Connect interceptor that:
// 1. Extracts and validates the JWT access token from Authorization header
// 2. Verifies the user is active and still has a non-revoked persistent session
// 3. Injects user ID into context
// 4. Enforces Casbin RBAC for protected procedures
//
//nolint:gocyclo // Authentication, session validation, and authorization precedence are explicit policy gates.
func NewAuthInterceptor(
	jwt *JWTManager,
	st store.Store,
	enforcer *Enforcer,
	publicMethods map[string]bool,
	logger *slog.Logger,
) connect.UnaryInterceptorFunc {
	if st == nil && enforcer != nil {
		st = enforcer.store
	}
	if publicMethods == nil {
		publicMethods = map[string]bool{}
	}
	if logger == nil {
		logger = slog.Default()
	}

	interceptor := func(next connect.UnaryFunc) connect.UnaryFunc {
		return connect.UnaryFunc(func(
			ctx context.Context,
			req connect.AnyRequest,
		) (connect.AnyResponse, error) {
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
			if !usesHandlerAuthorization(procedure) {
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
			}

			user, err := st.Users().Get(ctx, claims.UserID)
			if err != nil || user.Status != store.UserActive {
				return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("session revoked"))
			}
			active, err := hasActiveSession(ctx, st.AuthSessions(), claims.UserID)
			if err != nil {
				logger.Error("check active auth session failed", "error", err)
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("session validation failed"))
			}
			if !active {
				return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("session revoked"))
			}

			// Inject user ID into context.
			ctx = context.WithValue(ctx, userIDKey, claims.UserID)
			ctx = context.WithValue(ctx, rolesKey, claims.Roles)
			ctx = context.WithValue(ctx, orgIDKey, domain)
			ctx = authctx.WithActor(ctx, authctx.Actor{
				UserID: claims.UserID, OrganizationID: domain, Roles: claims.Roles,
			})
			return next(ctx, req)
		})
	}
	return interceptor
}

// Actor is the authenticated identity injected by NewAuthInterceptor.
type Actor = authctx.Actor

// ActorFromContext returns the authenticated actor snapshot.
func ActorFromContext(ctx context.Context) (Actor, bool) { return authctx.ActorFromContext(ctx) }

// ContextWithActor injects an authenticated actor for in-process callers and tests.
func ContextWithActor(ctx context.Context, actor Actor) context.Context {
	return authctx.WithActor(ctx, actor)
}

func hasActiveSession(ctx context.Context, sessions store.AuthSessionStore, userID string) (bool, error) {
	return sessions.HasActiveByUserID(ctx, userID)
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
		strings.HasPrefix(method, "Delete"), strings.HasPrefix(method, "Change"),
		strings.HasPrefix(method, "Emergency"), strings.HasPrefix(method, "Publish"),
		strings.HasPrefix(method, "Rollback"), strings.HasPrefix(method, "Configure"),
		strings.HasPrefix(method, "Sync"), strings.HasPrefix(method, "Logout"),
		strings.HasPrefix(method, "Refresh"), strings.HasPrefix(method, "Authenticate"),
		strings.HasPrefix(method, "Submit"), strings.HasPrefix(method, "Approve"),
		strings.HasPrefix(method, "Reject"):
		return "write"
	default:
		return ""
	}
}

func usesHandlerAuthorization(procedure string) bool {
	switch procedure {
	case orchestratorv1connect.OrchestratorServiceSubmitValuesRevisionProcedure,
		orchestratorv1connect.OrchestratorServiceApproveValuesRevisionProcedure,
		orchestratorv1connect.OrchestratorServiceRejectValuesRevisionProcedure:
		return true
	default:
		return false
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
