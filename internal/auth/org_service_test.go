package auth

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func setupOrgService(t *testing.T) (*OrgService, store.Store, func()) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	st, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := NewOrgService(st, logger)
	return svc, st, func() { st.Close() }
}

// seedUser creates a user in the users table (required for FK constraints on members).
// Idempotent: skips if the user already exists.
func seedUser(t *testing.T, st store.Store, userID string) {
	t.Helper()
	if _, err := st.Users().Get(context.Background(), userID); err == nil {
		return
	}
	require.NoError(t, st.Users().Create(context.Background(), &store.User{
		ID:       userID,
		Username: userID,
	}))
}

// seedPlatformAdmin creates a seed organization with a platform_admin member.
// Returns the platform_admin's userID and the orgID.
func seedPlatformAdmin(t *testing.T, st store.Store) (userID, orgID string) {
	t.Helper()
	ctx := context.Background()
	orgID = newID()
	userID = newID()

	seedUser(t, st, userID)

	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{
		ID:   orgID,
		Name: "Seed Org",
	}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID:  orgID,
		UserID: userID,
		Role:   store.RolePlatformAdmin,
	}))
	return
}

// seedReleaseAdmin creates an org with platform_admin + release_admin members.
// Returns the release_admin userID.
func seedReleaseAdmin(t *testing.T, st store.Store) (releaseAdminUserID, orgID string) {
	t.Helper()
	ctx := context.Background()
	orgID = newID()
	adminUserID := newID()
	releaseAdminUserID = newID()

	seedUser(t, st, adminUserID)
	seedUser(t, st, releaseAdminUserID)

	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{
		ID:   orgID,
		Name: "Release Admin Org",
	}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID:  orgID,
		UserID: adminUserID,
		Role:   store.RolePlatformAdmin,
	}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID:  orgID,
		UserID: releaseAdminUserID,
		Role:   store.RoleReleaseAdmin,
	}))
	return releaseAdminUserID, orgID
}

// seedOrgMember creates a user and adds them as member of an org.
func seedOrgMember(t *testing.T, st store.Store, orgID, userID string, role store.Role) {
	t.Helper()
	seedUser(t, st, userID)
	require.NoError(t, st.OrgMembers().Create(context.Background(), &store.OrganizationMember{
		OrgID:  orgID,
		UserID: userID,
		Role:   role,
	}))
}

func withUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// --- CreateOrganization ---

func TestCreateOrganization_PlatformAdmin_Success(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	svc, st, cleanup := setupOrgService(t)
	defer cleanup()
	adminID, _ := seedPlatformAdmin(t, st)

	ctx := withUser(context.Background(), adminID)
	resp, err := svc.CreateOrganization(ctx, connect.NewRequest(&authv1.CreateOrganizationRequest{
		Name: "acme-corp",
	}))
	must.NoError(err)
	is.NotEmpty(resp.Msg.Organization.Id)
	is.Equal("acme-corp", resp.Msg.Organization.Name)
	is.Equal("active", resp.Msg.Organization.Status)

	// Creator auto-added as platform_admin member.
	member, err := st.OrgMembers().Get(context.Background(), resp.Msg.Organization.Id, adminID)
	must.NoError(err)
	is.Equal(store.RolePlatformAdmin, member.Role)
}

func TestCreateOrganization_NonPlatformAdmin_Denied(t *testing.T) {
	is := assert.New(t)

	svc, _, cleanup := setupOrgService(t)
	defer cleanup()

	ctx := withUser(context.Background(), "random-user-001")
	_, err := svc.CreateOrganization(ctx, connect.NewRequest(&authv1.CreateOrganizationRequest{
		Name: "acme-corp",
	}))
	require.Error(t, err)
	is.Equal(connect.CodePermissionDenied, connect.CodeOf(err))
}

// --- GetOrganization ---

func TestGetOrganization(t *testing.T) {
	must := require.New(t)

	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	ctx := context.Background()
	org := &store.Organization{ID: newID(), Name: "acme-corp"}
	must.NoError(st.Organizations().Create(ctx, org))

	t.Run("found by ID", func(t *testing.T) {
		is := assert.New(t)
		resp, err := svc.GetOrganization(ctx, connect.NewRequest(&authv1.GetOrganizationRequest{
			OrgId: org.ID,
		}))
		must.NoError(err)
		is.Equal(org.ID, resp.Msg.Organization.Id)
		is.Equal("acme-corp", resp.Msg.Organization.Name)
	})

	t.Run("not found", func(t *testing.T) {
		is := assert.New(t)
		_, err := svc.GetOrganization(ctx, connect.NewRequest(&authv1.GetOrganizationRequest{
			OrgId: "nonexistent",
		}))
		is.Error(err)
		is.Equal(connect.CodeNotFound, connect.CodeOf(err))
	})
}

// --- ListOrganizations ---

func TestListOrganizations(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	ctx := context.Background()
	must.NoError(st.Organizations().Create(ctx, &store.Organization{ID: newID(), Name: "org-a"}))
	must.NoError(st.Organizations().Create(ctx, &store.Organization{ID: newID(), Name: "org-b"}))

	resp, err := svc.ListOrganizations(ctx, connect.NewRequest(&authv1.ListOrganizationsRequest{}))
	must.NoError(err)
	is.Len(resp.Msg.Organizations, 2)
}

// --- UpdateOrganization ---

func TestUpdateOrganization_Success(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	ctx := context.Background()
	org := &store.Organization{ID: newID(), Name: "original"}
	must.NoError(st.Organizations().Create(ctx, org))

	resp, err := svc.UpdateOrganization(ctx, connect.NewRequest(&authv1.UpdateOrganizationRequest{
		OrgId:           org.ID,
		Name:            "renamed",
		ExpectedVersion: 0,
	}))
	must.NoError(err)
	is.Equal("renamed", resp.Msg.Organization.Name)
}

func TestUpdateOrganization_OptimisticLock(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	ctx := context.Background()
	org := &store.Organization{ID: newID(), Name: "original"}
	must.NoError(st.Organizations().Create(ctx, org))

	// First update.
	_, err := svc.UpdateOrganization(ctx, connect.NewRequest(&authv1.UpdateOrganizationRequest{
		OrgId:           org.ID,
		Name:            "first-update",
		ExpectedVersion: 0,
	}))
	must.NoError(err)

	// Second update with stale version → conflict.
	_, err = svc.UpdateOrganization(ctx, connect.NewRequest(&authv1.UpdateOrganizationRequest{
		OrgId:           org.ID,
		Name:            "second-update",
		ExpectedVersion: 0,
	}))
	require.Error(t, err)
	is.Equal(connect.CodeAborted, connect.CodeOf(err))
	is.Contains(err.Error(), "optimistic lock conflict")
}

// --- DisableOrganization ---

func TestDisableOrganization_Success(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	ctx := context.Background()
	org := &store.Organization{ID: newID(), Name: "to-disable"}
	must.NoError(st.Organizations().Create(ctx, org))

	resp, err := svc.DisableOrganization(ctx, connect.NewRequest(&authv1.DisableOrganizationRequest{
		OrgId:           org.ID,
		ExpectedVersion: 0,
	}))
	must.NoError(err)
	is.Equal("disabled", resp.Msg.Organization.Status)
}

// --- AddMember ---

func TestAddMember(t *testing.T) {
	must := require.New(t)

	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	releaseAdminID, orgID := seedReleaseAdmin(t, st)

	t.Run("release_admin grants deployer", func(t *testing.T) {
		is := assert.New(t)
		ctx := withUser(context.Background(), releaseAdminID)
		targetUserID := newID()
		seedUser(t, st, targetUserID)

		resp, err := svc.AddMember(ctx, connect.NewRequest(&authv1.AddMemberRequest{
			OrgId:  orgID,
			UserId: targetUserID,
			Role:   string(store.RoleDeployer),
		}))
		must.NoError(err)
		is.Equal(orgID, resp.Msg.Member.OrgId)
		is.Equal(targetUserID, resp.Msg.Member.UserId)
		is.Equal(string(store.RoleDeployer), resp.Msg.Member.Role)
	})

	t.Run("release_admin grants viewer", func(t *testing.T) {
		is := assert.New(t)
		ctx := withUser(context.Background(), releaseAdminID)
		targetUserID := newID()
		seedUser(t, st, targetUserID)

		resp, err := svc.AddMember(ctx, connect.NewRequest(&authv1.AddMemberRequest{
			OrgId:  orgID,
			UserId: targetUserID,
			Role:   string(store.RoleViewer),
		}))
		must.NoError(err)
		is.Equal(string(store.RoleViewer), resp.Msg.Member.Role)
	})

	t.Run("release_admin cannot grant platform_admin (AC-026-01)", func(t *testing.T) {
		is := assert.New(t)
		ctx := withUser(context.Background(), releaseAdminID)
		targetUserID := newID()
		seedUser(t, st, targetUserID)

		_, err := svc.AddMember(ctx, connect.NewRequest(&authv1.AddMemberRequest{
			OrgId:  orgID,
			UserId: targetUserID,
			Role:   string(store.RolePlatformAdmin),
		}))
		require.Error(t, err)
		is.Equal(connect.CodePermissionDenied, connect.CodeOf(err))
		is.Contains(err.Error(), "cannot grant role")
	})

	t.Run("disabled organization rejects new members (AC-026-03)", func(t *testing.T) {
		is := assert.New(t)

		disabledOrg := &store.Organization{ID: newID(), Name: "disabled-org", Status: store.OrgDisabled}
		must.NoError(st.Organizations().Create(context.Background(), disabledOrg))
		// Add caller as release_admin in the disabled org so membership check passes.
		seedOrgMember(t, st, disabledOrg.ID, releaseAdminID, store.RoleReleaseAdmin)

		ctx := withUser(context.Background(), releaseAdminID)
		targetUserID := newID()
		seedUser(t, st, targetUserID)
		_, err := svc.AddMember(ctx, connect.NewRequest(&authv1.AddMemberRequest{
			OrgId:  disabledOrg.ID,
			UserId: targetUserID,
			Role:   string(store.RoleViewer),
		}))
		require.Error(t, err)
		is.Equal(connect.CodeFailedPrecondition, connect.CodeOf(err))
		is.Contains(err.Error(), "disabled")
	})

	t.Run("invalid role", func(t *testing.T) {
		is := assert.New(t)
		ctx := withUser(context.Background(), releaseAdminID)

		_, err := svc.AddMember(ctx, connect.NewRequest(&authv1.AddMemberRequest{
			OrgId:  orgID,
			UserId: newID(),
			Role:   "super_admin",
		}))
		require.Error(t, err)
		is.Equal(connect.CodeInvalidArgument, connect.CodeOf(err))
	})
}

// --- RemoveMember ---

func TestRemoveMember(t *testing.T) {
	must := require.New(t)

	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	_, orgID := seedPlatformAdmin(t, st)

	t.Run("remove non-last platform_admin", func(t *testing.T) {
		is := assert.New(t)

		secondAdminID := newID()
		seedOrgMember(t, st, orgID, secondAdminID, store.RolePlatformAdmin)

		ctx := withUser(context.Background(), secondAdminID)
		_, err := svc.RemoveMember(ctx, connect.NewRequest(&authv1.RemoveMemberRequest{
			OrgId:  orgID,
			UserId: secondAdminID,
		}))
		must.NoError(err)

		_, err = st.OrgMembers().Get(context.Background(), orgID, secondAdminID)
		is.ErrorIs(err, store.ErrNotFound)
	})

	t.Run("cannot remove last platform_admin (AC-026-02)", func(t *testing.T) {
		is := assert.New(t)

		soloOrgID := newID()
		soloAdminID := newID()
		seedUser(t, st, soloAdminID)
		must.NoError(st.Organizations().Create(context.Background(), &store.Organization{
			ID:   soloOrgID,
			Name: "Solo Admin Org",
		}))
		seedOrgMember(t, st, soloOrgID, soloAdminID, store.RolePlatformAdmin)

		ctx := withUser(context.Background(), soloAdminID)
		_, err := svc.RemoveMember(ctx, connect.NewRequest(&authv1.RemoveMemberRequest{
			OrgId:  soloOrgID,
			UserId: soloAdminID,
		}))
		require.Error(t, err)
		is.Equal(connect.CodeFailedPrecondition, connect.CodeOf(err))
		is.Contains(err.Error(), "last platform_admin")
	})

	t.Run("remove non-admin member", func(t *testing.T) {
		is := assert.New(t)

		viewerID := newID()
		viewerOrg := &store.Organization{ID: newID(), Name: "viewer-org"}
		must.NoError(st.Organizations().Create(context.Background(), viewerOrg))

		seedOrgMember(t, st, viewerOrg.ID, viewerID, store.RoleViewer)
		// Need a platform_admin to avoid "last admin" guard triggering.
		seedOrgMember(t, st, viewerOrg.ID, newID(), store.RolePlatformAdmin)

		ctx := withUser(context.Background(), viewerID)
		_, err := svc.RemoveMember(ctx, connect.NewRequest(&authv1.RemoveMemberRequest{
			OrgId:  viewerOrg.ID,
			UserId: viewerID,
		}))
		must.NoError(err)

		_, err = st.OrgMembers().Get(context.Background(), viewerOrg.ID, viewerID)
		is.ErrorIs(err, store.ErrNotFound)
	})
}

// --- ListMembers ---

func TestListMembers(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	ctx := context.Background()
	orgID := newID()
	must.NoError(st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "member-org"}))

	members := []struct {
		userID string
		role   store.Role
	}{
		{"user-a", store.RolePlatformAdmin},
		{"user-b", store.RoleViewer},
		{"user-c", store.RoleDeployer},
	}
	for _, m := range members {
		seedOrgMember(t, st, orgID, m.userID, m.role)
	}

	resp, err := svc.ListMembers(ctx, connect.NewRequest(&authv1.ListMembersRequest{
		OrgId: orgID,
	}))
	must.NoError(err)
	is.Len(resp.Msg.Members, 3)
}

// --- UpdateMemberRole ---

func TestUpdateMemberRole(t *testing.T) {
	must := require.New(t)

	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	releaseAdminID, orgID := seedReleaseAdmin(t, st)

	t.Run("release_admin promotes viewer to deployer", func(t *testing.T) {
		is := assert.New(t)

		viewerID := newID()
		seedOrgMember(t, st, orgID, viewerID, store.RoleViewer)

		ctx := withUser(context.Background(), releaseAdminID)
		resp, err := svc.UpdateMemberRole(ctx, connect.NewRequest(&authv1.UpdateMemberRoleRequest{
			OrgId:           orgID,
			UserId:          viewerID,
			NewRole:         string(store.RoleDeployer),
			ExpectedVersion: 0,
		}))
		must.NoError(err)
		is.Equal(string(store.RoleDeployer), resp.Msg.Member.Role)
	})

	t.Run("release_admin cannot grant platform_admin (AC-026-01)", func(t *testing.T) {
		is := assert.New(t)

		deployerID := newID()
		seedOrgMember(t, st, orgID, deployerID, store.RoleDeployer)

		ctx := withUser(context.Background(), releaseAdminID)
		_, err := svc.UpdateMemberRole(ctx, connect.NewRequest(&authv1.UpdateMemberRoleRequest{
			OrgId:           orgID,
			UserId:          deployerID,
			NewRole:         string(store.RolePlatformAdmin),
			ExpectedVersion: 0,
		}))
		require.Error(t, err)
		is.Equal(connect.CodePermissionDenied, connect.CodeOf(err))
	})

	t.Run("cannot demote last platform_admin (AC-026-02)", func(t *testing.T) {
		is := assert.New(t)

		soloOrgID := newID()
		soloAdminID := newID()
		seedUser(t, st, soloAdminID)
		must.NoError(st.Organizations().Create(context.Background(), &store.Organization{
			ID:   soloOrgID,
			Name: "Solo Admin Org",
		}))
		seedOrgMember(t, st, soloOrgID, soloAdminID, store.RolePlatformAdmin)

		ctx := withUser(context.Background(), soloAdminID)
		_, err := svc.UpdateMemberRole(ctx, connect.NewRequest(&authv1.UpdateMemberRoleRequest{
			OrgId:           soloOrgID,
			UserId:          soloAdminID,
			NewRole:         string(store.RoleViewer),
			ExpectedVersion: 0,
		}))
		require.Error(t, err)
		is.Equal(connect.CodeFailedPrecondition, connect.CodeOf(err))
		is.Contains(err.Error(), "last platform_admin")
	})

	t.Run("optimistic lock conflict (AC-026-04)", func(t *testing.T) {
		is := assert.New(t)

		deployerID := newID()
		seedOrgMember(t, st, orgID, deployerID, store.RoleDeployer)

		ctx := withUser(context.Background(), releaseAdminID)

		// First update: deployer → viewer (version 0→1 in DB).
		_, err := svc.UpdateMemberRole(ctx, connect.NewRequest(&authv1.UpdateMemberRoleRequest{
			OrgId:           orgID,
			UserId:          deployerID,
			NewRole:         string(store.RoleViewer),
			ExpectedVersion: 0,
		}))
		must.NoError(err)

		// Second update with stale version → conflict.
		_, err = svc.UpdateMemberRole(ctx, connect.NewRequest(&authv1.UpdateMemberRoleRequest{
			OrgId:           orgID,
			UserId:          deployerID,
			NewRole:         string(store.RoleDeployer),
			ExpectedVersion: 0,
		}))
		require.Error(t, err)
		is.Equal(connect.CodeAborted, connect.CodeOf(err))
		is.Contains(err.Error(), "optimistic lock conflict")
	})
}

var _ = (*OrgService)(nil)
