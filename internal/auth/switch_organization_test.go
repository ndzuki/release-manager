package auth

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// TestSwitchOrganization_EndToEnd covers TASK-095 AC-2 over the real HTTP +
// interceptor stack: a real JWT user switches the active organization, the
// returned token is scoped to the target organization, and a follow-up request
// actually resolves in that organization.
//
// Before the fix the call failed at the first gate (resolveDomain rejected the
// cross-organization target with permission_denied); after it, the second gate
// (an empty action for the "Switch" prefix) no longer exists either.
func TestSwitchOrganization_EndToEnd(t *testing.T) {
	h := newLocalUserHarness(t)
	t.Cleanup(h.server.Close)
	ctx := context.Background()

	require.NoError(t, h.st.Organizations().Create(ctx, &store.Organization{ID: "org-2", Name: "Org 2"}))
	require.NoError(t, h.st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-2", UserID: "admin-1", Role: store.RolePlatformAdmin,
	}))
	_, err := h.enforcer.RefreshPolicies(ctx)
	require.NoError(t, err)

	token := h.login(t, "admin")

	// Baseline: an empty org_id binds the new user to the session organization.
	before, err := h.createLocalUser(t, token, "before-switch", "pass", nil, "")
	require.NoError(t, err)
	assert.Equal(t, "org-1", before.GetOrgId())

	switchReq := connect.NewRequest(&authv1.SwitchOrganizationRequest{OrgId: "org-2"})
	switchReq.Header().Set("Authorization", "Bearer "+token)
	switched, err := h.client.SwitchOrganization(ctx, switchReq)
	require.NoError(t, err, "SwitchOrganization must succeed for a platform_admin member of the target organization")
	require.NotNil(t, switched.Msg.GetUser())
	assert.Equal(t, "org-2", switched.Msg.GetUser().GetActiveOrgId())
	assert.NotEmpty(t, switched.Msg.GetAccessToken())
	var targetListed bool
	for _, org := range switched.Msg.GetOrganizations() {
		if org.GetId() == "org-2" {
			targetListed = true
			assert.Equal(t, "Org 2", org.GetName())
		}
	}
	assert.True(t, targetListed, "the switch response must list the target organization")

	// The re-signed token carries the switched organization context.
	claims, err := h.svc.jwt.ValidateAccessToken(switched.Msg.GetAccessToken())
	require.NoError(t, err)
	assert.Equal(t, "org-2", claims.OrgID)
	assert.Equal(t, "admin-1", claims.UserID)

	// A follow-up request through the same interceptor resolves in org-2.
	after, err := h.createLocalUser(t, switched.Msg.GetAccessToken(), "after-switch", "pass", nil, "")
	require.NoError(t, err)
	assert.Equal(t, "org-2", after.GetOrgId())
}

// TestSwitchOrganization_AuthorizesOnTheTargetOrganization pins the semantics
// chosen in TASK-095: the Casbin decision uses the target organization's
// domain, so a member whose role in the target differs from the current
// organization is judged by the target role.
func TestSwitchOrganization_AuthorizesOnTheTargetOrganization(t *testing.T) {
	h := newLocalUserHarness(t)
	t.Cleanup(h.server.Close)
	ctx := context.Background()

	passwordHash, err := HashPassword("password")
	require.NoError(t, err)
	require.NoError(t, h.st.Users().Create(ctx, &store.User{
		ID: "multi-1", Username: "multi", PasswordHash: passwordHash,
	}))
	// viewer in org-1 (the session organization), release_admin in org-2.
	require.NoError(t, h.st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-1", UserID: "multi-1", Role: store.RoleViewer,
	}))
	require.NoError(t, h.st.Organizations().Create(ctx, &store.Organization{ID: "org-2", Name: "Org 2"}))
	require.NoError(t, h.st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-2", UserID: "multi-1", Role: store.RoleReleaseAdmin,
	}))
	_, err = h.enforcer.RefreshPolicies(ctx)
	require.NoError(t, err)

	token := h.login(t, "multi")

	// Into org-2 (release_admin → organization/write): allowed.
	switchReq := connect.NewRequest(&authv1.SwitchOrganizationRequest{OrgId: "org-2"})
	switchReq.Header().Set("Authorization", "Bearer "+token)
	switched, err := h.client.SwitchOrganization(ctx, switchReq)
	require.NoError(t, err)

	// Back into org-1 (viewer → no organization/write): denied on the target domain.
	backReq := connect.NewRequest(&authv1.SwitchOrganizationRequest{OrgId: "org-1"})
	backReq.Header().Set("Authorization", "Bearer "+switched.Msg.GetAccessToken())
	_, err = h.client.SwitchOrganization(ctx, backReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, "permission_denied", reasonFromConnectError(t, err))
}
