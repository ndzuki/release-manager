package audit

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// Tenant boundary for the release-api audit surface (TASK-095 AC-4).
//
// release-api has no Casbin policy source of its own (ADR-015: one authority
// per database), so the audit surface cannot re-run the organization-domain
// role matrix. What it can and must do is stop trusting the request for the
// tenant scope: the authenticated principal's organization is the only scope an
// audit read or export can address, and an emitted event may not claim another
// organization. Before this gate, QueryAuditEvents and ExportAuditEvents took
// organization_id straight from the request and Emit recorded whatever
// organization the event carried.

// principalOrganization returns the authenticated principal's organization.
func principalOrganization(ctx context.Context) (string, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("missing audit principal"))
	}
	if principal.OrgID == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("principal has no organization"))
	}
	return principal.OrgID, nil
}

// authorizeOrganizationScope resolves the organization scope of an audit
// request against the authenticated principal. An empty requested scope
// defaults to the principal's organization; a different organization is denied
// instead of being trusted from the request.
func authorizeOrganizationScope(ctx context.Context, requested string) (string, error) {
	organizationID, err := principalOrganization(ctx)
	if err != nil {
		return "", err
	}
	if requested != "" && requested != organizationID {
		denied := connect.NewError(connect.CodePermissionDenied,
			errors.New("organization scope does not match principal"))
		denied.Meta().Set("X-Reason-Code", "permission_denied")
		return "", denied
	}
	return organizationID, nil
}
