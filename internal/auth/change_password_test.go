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

// TestChangePassword_IsSelfService covers a second permanently-denied
// procedure the explicit registry gate surfaced: ChangePassword mapped to
// (auth, write), and the auth object has no non-wildcard policy row, so only
// platform_admin could ever change a password. The handler is self-service (it
// verifies the old password and revokes the caller's own sessions), so the
// interceptor now authenticates and leaves the decision to the handler.
func TestChangePassword_IsSelfService(t *testing.T) {
	h := newLocalUserHarness(t)
	t.Cleanup(h.server.Close)
	ctx := context.Background()

	passwordHash, err := HashPassword("password")
	require.NoError(t, err)
	require.NoError(t, h.st.Users().Create(ctx, &store.User{
		ID: "alice-id", Username: "alice", PasswordHash: passwordHash,
	}))
	require.NoError(t, h.st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-1", UserID: "alice-id", Role: store.RoleViewer,
	}))
	_, err = h.enforcer.RefreshPolicies(ctx)
	require.NoError(t, err)

	token := h.login(t, "alice")
	change := connect.NewRequest(&authv1.ChangePasswordRequest{
		OldPassword: "password", NewPassword: "rotated-password",
	})
	change.Header().Set("Authorization", "Bearer "+token)
	_, err = h.client.ChangePassword(ctx, change)
	require.NoError(t, err, "a viewer must be able to change their own password")

	_, err = h.client.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: "alice", Password: "password"}))
	require.Error(t, err, "the old password must stop working")

	rotated, err := h.client.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "alice", Password: "rotated-password",
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, rotated.Msg.GetAccessToken())
}
