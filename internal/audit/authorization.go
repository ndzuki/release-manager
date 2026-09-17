package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
)

// Tenant and role boundary for the release-api audit surface (TASK-095 AC-4,
// TASK-103 / ADR-021).
//
// release-api has no Casbin policy source of its own (ADR-015: one authority per
// database), so it does not decide roles locally. It forwards the caller's own
// access token to release-auth (the single authorization authority) and applies
// the scope that comes back: the effective organization, whether the caller may
// address another organization, and the policy-owned query window. Any failure to
// obtain a decision fails closed.

// Audit objects asked of release-auth.
const (
	auditObject = "audit"
	auditRead   = "read"
	auditWrite  = "write"
)

// resolveAuditScope asks release-auth for a decision on (object, action) and
// returns the effective organization scope. An empty requested organization
// means "the caller's own organization"; a different organization is allowed
// only when the decision says the caller may cross organizations
// (platform_admin, REQ-029).
func resolveAuditScope(
	ctx context.Context,
	decisions DecisionClient,
	requestedOrganization, object, action string,
) (AccessDecision, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return AccessDecision{}, connect.NewError(connect.CodeUnauthenticated, errors.New("missing audit principal"))
	}
	if principal.OrgID == "" {
		return AccessDecision{}, connect.NewError(connect.CodeUnauthenticated, errors.New("principal has no organization"))
	}
	if decisions == nil {
		return AccessDecision{}, connect.NewError(connect.CodeUnavailable, errors.New("authorization decision client is not configured"))
	}
	decision, err := decisions.Authorize(ctx, principal.Authorization, requestedOrganization, object, action)
	if err != nil {
		// Fail closed: an unreachable, slow, or erroring authority never falls back
		// to a local role guess.
		return AccessDecision{}, connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("authorization decision unavailable: %w", err))
	}
	if !decision.Allowed {
		reason := decision.Reason
		if reason == "" {
			reason = "permission_denied"
		}
		denied := connect.NewError(connect.CodePermissionDenied, errors.New("access denied"))
		denied.Meta().Set("X-Reason-Code", reason)
		if decision.PolicyVersion > 0 {
			denied.Meta().Set("X-Policy-Version", fmt.Sprintf("%d", decision.PolicyVersion))
		}
		return AccessDecision{}, denied
	}
	if decision.OrganizationID == "" {
		return AccessDecision{}, connect.NewError(connect.CodeUnavailable, errors.New("authorization decision has no organization scope"))
	}
	return decision, nil
}

// enforceAuditWindow rejects a query or export whose time range exceeds the
// policy-owned ceiling (REQ-029 AC-029-02: CodeInvalidArgument / range_too_large).
func enforceAuditWindow(since, until *time.Time, maxWindowDays int32, now time.Time) error {
	if maxWindowDays <= 0 {
		return nil
	}
	var span time.Duration
	switch {
	case since != nil && until != nil:
		span = until.Sub(*since)
	case since != nil:
		span = now.Sub(*since)
	default:
		return nil
	}
	if span <= time.Duration(maxWindowDays)*24*time.Hour {
		return nil
	}
	err := connect.NewError(connect.CodeInvalidArgument, errors.New("requested time range exceeds the allowed window"))
	err.Meta().Set("X-Reason-Code", "range_too_large")
	return err
}
