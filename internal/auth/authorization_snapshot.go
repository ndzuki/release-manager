package auth

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/store"
)

// AuthorizationService publishes server-authoritative scoped authorization snapshots.
type AuthorizationService struct {
	store    store.Store
	enforcer *Enforcer
	metrics  *authorization.Metrics
	logger   *slog.Logger
}

// NewAuthorizationService creates the REQ-027 Authorization Snapshot producer.
func NewAuthorizationService(st store.Store, enforcer *Enforcer, metrics *authorization.Metrics, logger *slog.Logger) *AuthorizationService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AuthorizationService{store: st, enforcer: enforcer, metrics: metrics, logger: logger}
}

// GetAuthorizationSnapshot returns one actor/scope projection; actor fields never come from the request.
//
//nolint:gocyclo // REQ-027 error-model gates are intentionally ordered and explicit.
func (s *AuthorizationService) GetAuthorizationSnapshot(
	ctx context.Context,
	req *connect.Request[authv1.GetAuthorizationSnapshotRequest],
) (*connect.Response[authv1.GetAuthorizationSnapshotResponse], error) {
	ctx, span := otel.Tracer("release-manager/auth").Start(ctx, "AuthorizationService.GetAuthorizationSnapshot")
	defer span.End()
	started := time.Now()
	defer func() {
		if s.metrics != nil {
			s.metrics.SnapshotRPCDuration.Observe(time.Since(started).Seconds())
		}
	}()

	actor, ok := authctx.ActorFromContext(ctx)
	if !ok || actor.OrganizationID == "" || (actor.UserID == "" && actor.Service == "") {
		return nil, snapshotError(connect.CodeUnauthenticated, "INVALID_ACTOR_CONTEXT", 0, 0, errors.New("invalid actor context"))
	}
	organizationID := req.Msg.GetOrganizationId()
	customerID := req.Msg.GetCustomerId()
	if uuid.Validate(organizationID) != nil || uuid.Validate(customerID) != nil {
		return nil, snapshotError(connect.CodeInvalidArgument, "INVALID_SCOPE", 0, 0, errors.New("organization_id and customer_id must be UUIDs"))
	}
	if organizationID != actor.OrganizationID {
		return nil, snapshotError(connect.CodePermissionDenied, "PERMISSION_DENIED", 0, 0, errors.New("scope is not available"))
	}

	durable, err := s.store.Authorization().Load(ctx)
	if err != nil {
		return nil, snapshotError(connect.CodeUnavailable, "POLICY_UNAVAILABLE", 0, 0, errors.New("authorization state unavailable"))
	}
	organization, err := s.store.Organizations().Get(ctx, organizationID)
	if err != nil || organization.Status != store.OrgActive {
		return nil, snapshotError(connect.CodePermissionDenied, "ORGANIZATION_DISABLED", durable.SourceVersion, durable.PolicyVersion, errors.New("scope is not available"))
	}
	customer, err := s.store.Customers().Get(ctx, customerID)
	if err != nil || customer.Status != store.CustomerActive {
		return nil, snapshotError(connect.CodePermissionDenied, "CUSTOMER_DISABLED", durable.SourceVersion, durable.PolicyVersion, errors.New("scope is not available"))
	}
	binding, err := s.store.Bindings().GetByOrgAndCustomer(ctx, organizationID, customerID)
	if err != nil || binding.Status != store.BindingActive {
		return nil, snapshotError(connect.CodePermissionDenied, "DOMAIN_BINDING_MISSING", durable.SourceVersion, durable.PolicyVersion, errors.New("scope is not available"))
	}

	subject := actor.UserID
	role := "service"
	if actor.Service == "" {
		member, memberErr := s.store.OrgMembers().Get(ctx, organizationID, actor.UserID)
		if memberErr != nil {
			return nil, snapshotError(connect.CodePermissionDenied, "PERMISSION_DENIED", durable.SourceVersion, durable.PolicyVersion, errors.New("scope is not available"))
		}
		role = string(member.Role)
	} else {
		subject = "service:" + actor.Service
	}
	if !s.enforcer.PolicyHealthy() {
		return nil, snapshotError(connect.CodeUnavailable, "POLICY_UNAVAILABLE", durable.SourceVersion, durable.PolicyVersion, errors.New("authorization policy unavailable"))
	}

	response := &authv1.GetAuthorizationSnapshotResponse{
		OrganizationId: organizationID,
		CustomerId:     customerID,
		BindingActive:  true,
		CustomerActive: true,
		Role:           role,
		ActorId:        subject,
		SourceVersion:  durable.SourceVersion,
		PolicyVersion:  durable.PolicyVersion,
		Checkpoint:     durable.SourceVersion,
		Fresh:          durable.SourceVersion > 0,
	}
	response.CanExecuteEmergency = s.allowed(ctx, subject, organizationID, store.AuthorizationExecuteEmergency)
	response.CanResolveEmergency = s.allowed(ctx, subject, organizationID, store.AuthorizationResolveEmergency)
	response.CanCreateValuesRevision = s.allowed(ctx, subject, organizationID, store.AuthorizationCreateValues)
	response.CanApproveValuesRevision = s.allowed(ctx, subject, organizationID, store.AuthorizationApproveValues)
	return connect.NewResponse(response), nil
}

// SetCapabilityGrant atomically grants, revokes, or reactivates one explicit capability.
//
//nolint:gocyclo // Retry loop plus REQ-027 validation gates stay explicit.
func (s *AuthorizationService) SetCapabilityGrant(
	ctx context.Context,
	req *connect.Request[authv1.SetCapabilityGrantRequest],
) (*connect.Response[authv1.SetCapabilityGrantResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok || actor.Service != "" || actor.UserID == "" || actor.OrganizationID == "" {
		return nil, snapshotError(connect.CodeUnauthenticated, "INVALID_ACTOR_CONTEXT", 0, 0, errors.New("invalid actor context"))
	}
	msg := req.Msg
	if uuid.Validate(msg.GetOrganizationId()) != nil || uuid.Validate(msg.GetSubject()) != nil {
		return nil, snapshotError(connect.CodeInvalidArgument, "INVALID_SCOPE", 0, 0, errors.New("organization_id and subject must be UUIDs"))
	}
	if msg.GetOrganizationId() != actor.OrganizationID {
		return nil, snapshotError(connect.CodePermissionDenied, "PERMISSION_DENIED", 0, 0, errors.New("scope is not available"))
	}
	action := store.AuthorizationAction(msg.GetAction())
	if !action.Valid() {
		return nil, snapshotError(connect.CodeInvalidArgument, "INVALID_ACTION", 0, 0, errors.New("action is not supported"))
	}
	member, err := s.store.OrgMembers().Get(ctx, actor.OrganizationID, actor.UserID)
	if err != nil || (member.Role != store.RolePlatformAdmin && member.Role != store.RoleReleaseAdmin) {
		return nil, snapshotError(connect.CodePermissionDenied, "PERMISSION_DENIED", 0, 0, errors.New("capability grant requires an administrator"))
	}
	if _, err := s.store.OrgMembers().Get(ctx, actor.OrganizationID, msg.GetSubject()); err != nil {
		return nil, snapshotError(connect.CodePermissionDenied, "PERMISSION_DENIED", 0, 0, errors.New("subject is not an active organization member"))
	}

	for attempt := 0; attempt < 3; attempt++ {
		durable, loadErr := s.store.Authorization().Load(ctx)
		if loadErr != nil {
			return nil, snapshotError(connect.CodeUnavailable, "POLICY_UNAVAILABLE", 0, 0, errors.New("authorization state unavailable"))
		}
		grants := upsertCapabilityGrant(durable.Grants, store.CapabilityGrant{
			OrganizationID: actor.OrganizationID,
			Subject:        msg.GetSubject(),
			Action:         action,
			GrantedBy:      actor.UserID,
			Revoked:        msg.GetRevoked(),
			UpdatedAt:      time.Now().UTC(),
		})
		rules, compileErr := compileAuthorizationRules(ctx, s.store, grants, durable.Rules)
		if compileErr != nil {
			return nil, snapshotError(connect.CodeUnavailable, "POLICY_UNAVAILABLE", durable.SourceVersion, durable.PolicyVersion, errors.New("authorization policy unavailable"))
		}
		applied, applyErr := s.store.Authorization().Apply(ctx, store.AuthorizationApplyCommand{
			ExpectedSourceVersion: durable.SourceVersion,
			ExpectedPolicyVersion: durable.PolicyVersion,
			Mutation:              store.AuthorizationGrantChanged,
			Grants:                grants,
			Rules:                 rules,
		})
		if errors.Is(applyErr, store.ErrOptimisticLock) {
			continue
		}
		if applyErr != nil {
			return nil, snapshotError(connect.CodeUnavailable, "POLICY_UNAVAILABLE", durable.SourceVersion, durable.PolicyVersion, errors.New("authorization grant update failed"))
		}
		if err := s.enforcer.LoadPolicies(ctx); err != nil {
			return nil, snapshotError(connect.CodeUnavailable, "POLICY_UNAVAILABLE", applied.SourceVersion, applied.PolicyVersion, errors.New("authorization policy reload failed"))
		}
		return connect.NewResponse(&authv1.SetCapabilityGrantResponse{
			SourceVersion: applied.SourceVersion,
			PolicyVersion: applied.PolicyVersion,
		}), nil
	}
	return nil, snapshotError(connect.CodeAborted, "AUTHORIZATION_VERSION_CONFLICT", 0, 0, errors.New("authorization version conflict"))
}

func upsertCapabilityGrant(grants []store.CapabilityGrant, update store.CapabilityGrant) []store.CapabilityGrant {
	result := make([]store.CapabilityGrant, 0, len(grants)+1)
	found := false
	for _, grant := range grants {
		if grant.OrganizationID == update.OrganizationID && grant.Subject == update.Subject && grant.Action == update.Action {
			if update.CreatedAt.IsZero() {
				update.CreatedAt = grant.CreatedAt
			}
			result = append(result, update)
			found = true
			continue
		}
		result = append(result, grant)
	}
	if !found {
		if update.CreatedAt.IsZero() {
			update.CreatedAt = update.UpdatedAt
		}
		result = append(result, update)
	}
	return result
}

func (s *AuthorizationService) allowed(ctx context.Context, subject, domain string, action store.AuthorizationAction) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	return s.enforcer.Enforce(subject, domain, "release", string(action)) == nil
}

func snapshotError(code connect.Code, reason string, source, policy uint64, cause error) error {
	err := connect.NewError(code, cause)
	err.Meta().Set("X-Reason-Code", reason)
	err.Meta().Set("X-Source-Version", strconv.FormatUint(source, 10))
	err.Meta().Set("X-Checkpoint-Fresh", strconv.FormatBool(source > 0))
	err.Meta().Set("X-Policy-Version", strconv.FormatUint(policy, 10))
	return err
}

func capabilityRules(member *store.OrganizationMember, grants []store.CapabilityGrant) []store.CasbinRule {
	rules := roleCapabilityRules(member.Role, member.OrgID)
	for _, grant := range grants {
		if grant.Revoked || grant.OrganizationID != member.OrgID || grant.Subject != member.UserID {
			continue
		}
		rules = append(rules, store.CasbinRule{
			PType: "p", V0: grant.Subject, V1: grant.OrganizationID, V2: "release", V3: string(grant.Action),
		})
	}
	return rules
}

func roleCapabilityRules(role store.Role, domain string) []store.CasbinRule {
	var actions []store.AuthorizationAction
	switch role {
	case store.RolePlatformAdmin:
		actions = []store.AuthorizationAction{
			store.AuthorizationExecuteEmergency, store.AuthorizationResolveEmergency,
			store.AuthorizationCreateValues, store.AuthorizationApproveValues,
		}
	case store.RoleReleaseAdmin:
		actions = []store.AuthorizationAction{
			store.AuthorizationExecuteEmergency, store.AuthorizationCreateValues, store.AuthorizationApproveValues,
		}
	case store.RoleDeployer:
		actions = []store.AuthorizationAction{store.AuthorizationCreateValues}
	}
	rules := make([]store.CasbinRule, 0, len(actions))
	for _, action := range actions {
		rules = append(rules, store.CasbinRule{PType: "p", V0: string(role), V1: domain, V2: "release", V3: string(action)})
	}
	return rules
}

var _ authv1connect.AuthorizationServiceHandler = (*AuthorizationService)(nil)
