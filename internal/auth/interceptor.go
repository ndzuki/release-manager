package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"

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
			cookieAuthenticated := false
			if token == "" {
				token = cookieValue(req.Header(), AccessCookieName)
				cookieAuthenticated = token != ""
			}
			if token == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing authentication credentials"))
			}

			claims, err := jwt.ValidateAccessToken(token)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid token: %w", err))
			}

			policy, registered := lookupProcedure(procedure)
			domain, err := resolveDomain(req.Any(), claims.OrgID, policy.targetOrg)
			if err != nil {
				return nil, authorizationConnectError(err, enforcer.PolicyVersion())
			}
			if !registered {
				return nil, authorizationConnectError(newInvalidActorContext(
					claims.UserID,
					domain,
					fmt.Errorf("unmapped procedure %q", procedure),
				), enforcer.PolicyVersion())
			}
			if cookieAuthenticated && policy.action != "read" {
				cookieToken := cookieValue(req.Header(), CSRFCookieName)
				headerToken := req.Header().Get(CSRFHeaderName)
				if cookieToken == "" || headerToken == "" || subtle.ConstantTimeCompare([]byte(cookieToken), []byte(headerToken)) != 1 {
					return nil, connect.NewError(connect.CodePermissionDenied, errors.New("csrf token mismatch"))
				}
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
			if policy.mode == modeCasbin {
				if err := enforcer.Enforce(claims.UserID, domain, policy.object, policy.action); err != nil {
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
			ctx = authctx.WithAuthorizationHeader(ctx, req.Header().Get("Authorization"))
			return next(ctx, req)
		})
	}
	return interceptor
}

// NewAuthStreamInterceptor applies the same authentication and RBAC policy to streaming RPCs.
func NewAuthStreamInterceptor(
	jwt *JWTManager,
	st store.Store,
	enforcer *Enforcer,
	publicMethods map[string]bool,
	logger *slog.Logger,
) connect.Interceptor {
	if st == nil && enforcer != nil {
		st = enforcer.store
	}
	if publicMethods == nil {
		publicMethods = map[string]bool{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return streamAuthInterceptor{jwt: jwt, store: st, enforcer: enforcer, publicMethods: publicMethods, logger: logger}
}

type streamAuthInterceptor struct {
	jwt           *JWTManager
	store         store.Store
	enforcer      *Enforcer
	publicMethods map[string]bool
	logger        *slog.Logger
}

func (i streamAuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }

func (i streamAuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i streamAuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		procedure := conn.Spec().Procedure
		if i.publicMethods[procedure] {
			return next(ctx, conn)
		}
		token := extractToken(conn.RequestHeader().Get("Authorization"))
		if token == "" {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("missing authorization header"))
		}
		claims, err := i.jwt.ValidateAccessToken(token)
		if err != nil {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid token: %w", err))
		}
		domain := claims.OrgID
		if domain == "" {
			return authorizationConnectError(newInvalidActorContext(claims.UserID, "", errors.New("organization is required")), i.enforcer.PolicyVersion())
		}
		policy, registered := lookupProcedure(procedure)
		if !registered {
			return authorizationConnectError(newInvalidActorContext(claims.UserID, domain, fmt.Errorf("unmapped procedure %q", procedure)), i.enforcer.PolicyVersion())
		}
		if policy.mode == modeCasbin {
			if err := i.enforcer.Enforce(claims.UserID, domain, policy.object, policy.action); err != nil {
				i.logger.Warn("stream access denied", "user_id", claims.UserID, "organization_id", domain,
					"procedure", procedure, "reason_code", authorizationReason(err))
				return authorizationConnectError(err, i.enforcer.PolicyVersion())
			}
		}
		user, err := i.store.Users().Get(ctx, claims.UserID)
		if err != nil || user.Status != store.UserActive {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("session revoked"))
		}
		active, err := hasActiveSession(ctx, i.store.AuthSessions(), claims.UserID)
		if err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("session validation failed"))
		}
		if !active {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("session revoked"))
		}
		ctx = authctx.WithActor(ctx, authctx.Actor{UserID: claims.UserID, OrganizationID: domain, Roles: claims.Roles})
		return next(ctx, conn)
	}
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

// resolveDomain returns the organization domain used for the Casbin decision.
// The request org_id may differ from the token org only for a procedure
// registered with targetOrg (SwitchOrganization): there the request field names
// the organization being switched into, and the handler verifies membership.
func resolveDomain(request any, tokenOrgID string, targetOrg bool) (string, error) {
	requestOrgID := protoStringField(request, "org_id")
	if requestOrgID != "" {
		if tokenOrgID != "" && tokenOrgID != requestOrgID && !targetOrg {
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

// mapProcedure and the procedure→authorization registry live in
// procedure_policy.go: the mapping is explicit per procedure, with no
// string-prefix fallback.

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
