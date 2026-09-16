package auth

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// Authorization decision contract (ADR-021, REQ-029).
//
// release-api serves the audit surface from a database that holds no membership
// or policy rows (ADR-015: one authority per database), so it forwards the
// caller's own access token here and applies the returned scope. This handler is
// the single decision point: it resolves the caller's persistent membership role
// in its session organization, enforces (object, action) through Casbin for the
// caller's own organization, and only widens the scope to another organization
// for platform_admin, keeping the cross-organization and window policy in this
// service instead of in a consumer.

const (
	// decisionReasonOK marks an allowed decision.
	decisionReasonOK = "ok"
	// decisionReasonCrossOrganizationDenied refuses a target organization other
	// than the caller's session organization for a non-platform_admin.
	decisionReasonCrossOrganizationDenied = "cross_organization_denied"
)

// Window ceilings are policy owned by this service (REQ-029 授权规则): the caller
// must apply the returned value rather than hardcoding roles.
const (
	defaultMaxWindowDays       = 31
	platformAdminMaxWindowDays = 366
)

// AuthorizeAccess answers whether the caller may act with (object, action) in one
// organization, and returns the scope the caller must apply.
func (s *AuthorizationService) AuthorizeAccess(
	ctx context.Context,
	req *connect.Request[authv1.AuthorizeAccessRequest],
) (*connect.Response[authv1.AuthorizeAccessResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok || actor.UserID == "" || actor.OrganizationID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid actor context"))
	}
	object := req.Msg.GetObject()
	action := req.Msg.GetAction()
	if object == "" || action == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("object and action are required"))
	}
	if !s.enforcer.PolicyHealthy() {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("authorization policy unavailable"))
	}
	// The role always comes from the caller's session organization: that is the
	// server-authoritative identity (ADR-006), never a request field.
	member, err := s.store.OrgMembers().Get(ctx, actor.OrganizationID, actor.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("actor has no active membership"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("membership lookup: %w", err))
	}

	targetOrg := req.Msg.GetOrganizationId()
	if targetOrg == "" {
		targetOrg = actor.OrganizationID
	}
	response := newAccessDecision(member, targetOrg, s.enforcer.PolicyVersion())

	if targetOrg != actor.OrganizationID {
		// Only platform_admin reaches beyond its session organization; every other
		// role is refused before the policy is consulted (REQ-029 授权规则).
		if member.Role != store.RolePlatformAdmin {
			response.Reason = decisionReasonCrossOrganizationDenied
			return connect.NewResponse(response), nil
		}
		response.Allowed = true
		response.Reason = decisionReasonOK
		return connect.NewResponse(response), nil
	}

	allowed, reason, err := s.decideOwnOrganization(actor, targetOrg, object, action)
	if err != nil {
		return nil, err
	}
	response.Allowed = allowed
	response.Reason = reason
	return connect.NewResponse(response), nil
}

// newAccessDecision seeds the response with the scope policy this service owns:
// the effective organization, the caller's role, and the window ceiling.
func newAccessDecision(
	member *store.OrganizationMember,
	targetOrg string,
	policyVersion uint64,
) *authv1.AuthorizeAccessResponse {
	platformAdmin := member.Role == store.RolePlatformAdmin
	response := &authv1.AuthorizeAccessResponse{
		OrganizationId:         targetOrg,
		Role:                   string(member.Role),
		PolicyVersion:          policyVersion,
		AllowCrossOrganization: platformAdmin,
		MaxWindowDays:          defaultMaxWindowDays,
	}
	if platformAdmin {
		response.MaxWindowDays = platformAdminMaxWindowDays
	}
	return response
}

// decideOwnOrganization enforces (object, action) for the caller's own
// organization and maps the failure to a reason code. An unreadable policy stays
// an RPC error so the caller fails closed instead of reading it as a denial.
func (s *AuthorizationService) decideOwnOrganization(
	actor authctx.Actor,
	targetOrg, object, action string,
) (allowed bool, reason string, err error) {
	err = s.enforcer.Enforce(actor.UserID, targetOrg, object, action)
	if err == nil {
		return true, decisionReasonOK, nil
	}
	var unavailable *PolicyUnavailableError
	if errors.As(err, &unavailable) {
		return false, "", connect.NewError(connect.CodeUnavailable, errors.New("authorization policy unavailable"))
	}
	reason = authorizationReason(err)
	if reason == "" {
		reason = "permission_denied"
	}
	return false, reason, nil
}
