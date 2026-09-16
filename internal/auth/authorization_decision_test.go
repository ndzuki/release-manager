package auth

import (
	"context"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// TestAuthorizeAccessDecisionMatrix covers the ADR-021 decision RPC release-api
// depends on: the role comes from the caller's persistent membership, the
// cross-organization widening is platform_admin-only, and the window ceiling is
// policy-owned by this service.
func TestAuthorizeAccessDecisionMatrix(t *testing.T) {
	enforcer, st := setupEnforcer(t)
	ctx := context.Background()
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-1", Name: "Org 1"}))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-2", Name: "Org 2"}))
	for _, member := range []*store.OrganizationMember{
		{OrgID: "org-1", UserID: "user-viewer", Role: store.RoleViewer},
		{OrgID: "org-1", UserID: "user-admin", Role: store.RoleReleaseAdmin},
		{OrgID: "org-1", UserID: "user-platform", Role: store.RolePlatformAdmin},
	} {
		require.NoError(t, st.Users().Create(ctx, &store.User{ID: member.UserID, Username: member.UserID, PasswordHash: "h"}))
		require.NoError(t, st.OrgMembers().Create(ctx, member))
	}
	require.NoError(t, enforcer.LoadPolicies(ctx))

	service := NewAuthorizationService(st, enforcer, nil, slog.New(slog.DiscardHandler))
	actorCtx := func(userID string) context.Context {
		return authctx.WithActor(context.Background(), authctx.Actor{
			UserID: userID, OrganizationID: "org-1", Roles: []string{"viewer"},
		})
	}
	call := func(t *testing.T, userID, org, object, action string) (*connect.Response[authv1.AuthorizeAccessResponse], error) {
		t.Helper()
		return service.AuthorizeAccess(actorCtx(userID), connect.NewRequest(&authv1.AuthorizeAccessRequest{
			OrganizationId: org, Object: object, Action: action,
		}))
	}

	t.Run("viewer reads its own organization", func(t *testing.T) {
		response, err := call(t, "user-viewer", "", "audit", "read")
		require.NoError(t, err)
		assert.True(t, response.Msg.GetAllowed())
		assert.Equal(t, decisionReasonOK, response.Msg.GetReason())
		assert.Equal(t, "org-1", response.Msg.GetOrganizationId())
		assert.Equal(t, string(store.RoleViewer), response.Msg.GetRole())
		assert.False(t, response.Msg.GetAllowCrossOrganization())
		assert.Equal(t, int32(defaultMaxWindowDays), response.Msg.GetMaxWindowDays())
	})

	t.Run("viewer may not write the audit trail", func(t *testing.T) {
		response, err := call(t, "user-viewer", "", "audit", "write")
		require.NoError(t, err)
		assert.False(t, response.Msg.GetAllowed())
		assert.Equal(t, "permission_denied", response.Msg.GetReason())
	})

	t.Run("release admin may write the audit trail", func(t *testing.T) {
		response, err := call(t, "user-admin", "", "audit", "write")
		require.NoError(t, err)
		assert.True(t, response.Msg.GetAllowed())
	})

	t.Run("non platform admin may not cross organizations", func(t *testing.T) {
		response, err := call(t, "user-admin", "org-2", "audit", "read")
		require.NoError(t, err)
		assert.False(t, response.Msg.GetAllowed())
		assert.Equal(t, decisionReasonCrossOrganizationDenied, response.Msg.GetReason())
		assert.Equal(t, "org-2", response.Msg.GetOrganizationId())
	})

	t.Run("platform admin may cross organizations with the wider window", func(t *testing.T) {
		response, err := call(t, "user-platform", "org-2", "audit", "read")
		require.NoError(t, err)
		assert.True(t, response.Msg.GetAllowed())
		assert.True(t, response.Msg.GetAllowCrossOrganization())
		assert.Equal(t, int32(platformAdminMaxWindowDays), response.Msg.GetMaxWindowDays())
		assert.Equal(t, "org-2", response.Msg.GetOrganizationId())
	})

	t.Run("missing object or action is invalid", func(t *testing.T) {
		_, err := call(t, "user-viewer", "", "audit", "")
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})

	t.Run("no actor is unauthenticated", func(t *testing.T) {
		_, err := service.AuthorizeAccess(context.Background(), connect.NewRequest(&authv1.AuthorizeAccessRequest{
			Object: "audit", Action: "read",
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("caller without membership is denied", func(t *testing.T) {
		_, err := call(t, "user-unknown", "", "audit", "read")
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})
}
