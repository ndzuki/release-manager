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

// reasonCodeOf reads the stable reason code a handler attached to a Connect
// error. REQ-026 names its domain errors (organization_disabled,
// last_platform_admin_forbidden, optimistic_lock_conflict) and a client is meant
// to branch on the code rather than on message text.
func reasonCodeOf(t *testing.T, err error) string {
	t.Helper()
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	return connectErr.Meta().Get("X-Reason-Code")
}

// AC-026-03: adding a member to a disabled organization reports
// organization_disabled. The handler previously returned a bare
// CodeFailedPrecondition, so a client could not tell this apart from any other
// precondition failure.
func TestAddMember_DisabledOrganizationReasonCode(t *testing.T) {
	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	adminID, orgID := seedReleaseAdmin(t, st)
	org, err := st.Organizations().Get(context.Background(), orgID)
	require.NoError(t, err)
	org.Status = store.OrgDisabled
	require.NoError(t, st.Organizations().Update(context.Background(), org))

	_, err = svc.AddMember(withUser(context.Background(), adminID), connect.NewRequest(&authv1.AddMemberRequest{
		OrgId:  orgID,
		UserId: newID(),
		Role:   string(store.RoleViewer),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, reasonOrganizationDisabled, reasonCodeOf(t, err))
}

// AC-026-02: removing the last platform_admin reports
// last_platform_admin_forbidden.
func TestRemoveMember_LastAdminReasonCode(t *testing.T) {
	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	ctx := context.Background()
	orgID := newID()
	adminID := newID()
	seedUser(t, st, adminID)
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Solo Admin Org"}))
	seedOrgMember(t, st, orgID, adminID, store.RolePlatformAdmin)

	_, err := svc.RemoveMember(withUser(ctx, adminID), connect.NewRequest(&authv1.RemoveMemberRequest{
		OrgId:  orgID,
		UserId: adminID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, reasonLastPlatformAdminForbidden, reasonCodeOf(t, err))
}

// AC-026-04: a stale expected_version reports optimistic_lock_conflict.
func TestUpdateOrganization_OptimisticLockReasonCode(t *testing.T) {
	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	ctx := context.Background()
	org, err := st.Organizations().Get(ctx, seedReleaseAdminOrgID(t, st))
	require.NoError(t, err)

	_, err = svc.UpdateOrganization(ctx, connect.NewRequest(&authv1.UpdateOrganizationRequest{
		OrgId:           org.ID,
		Name:            "renamed",
		ExpectedVersion: org.OptimisticVersion + 99,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	assert.Equal(t, reasonOptimisticLockConflict, reasonCodeOf(t, err))
}

// seedReleaseAdminOrgID returns the organization the seeded release_admin belongs
// to, for tests that only need a valid organization id.
func seedReleaseAdminOrgID(t *testing.T, st store.Store) string {
	t.Helper()
	_, orgID := seedReleaseAdmin(t, st)
	return orgID
}
