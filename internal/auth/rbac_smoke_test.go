package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

func TestRBACSmoke_NextRequestUsesUpdatedPolicy(t *testing.T) {
	enforcer, st := setupEnforcer(t)
	ctx := context.Background()
	passwordHash, err := HashPassword("password")
	require.NoError(t, err)
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-1", Name: "Org 1"}))
	require.NoError(t, st.Users().Create(ctx, &store.User{
		ID: "admin-1", Username: "admin", PasswordHash: passwordHash,
	}))
	require.NoError(t, st.Users().Create(ctx, &store.User{
		ID: "user-1", Username: "alice", PasswordHash: passwordHash,
	}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-1", UserID: "admin-1", Role: store.RolePlatformAdmin,
	}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-1", UserID: "user-1", Role: store.RoleViewer,
	}))
	require.NoError(t, enforcer.LoadPolicies(ctx))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	jwtManager := NewJWTManager([]byte("test-signing-key"), time.Hour, time.Hour)
	interceptor := NewAuthInterceptor(jwtManager, st, enforcer, map[string]bool{
		authv1connect.AuthServiceLoginProcedure: true,
	}, logger)
	mux := http.NewServeMux()
	authPath, authHandler := authv1connect.NewAuthServiceHandler(
		NewAuthService(st, jwtManager, NewRateLimiter(10, time.Minute), logger),
		connect.WithInterceptors(interceptor),
	)
	mux.Handle(authPath, authHandler)
	orgPath, orgHandler := authv1connect.NewOrganizationServiceHandler(
		NewOrgService(st, logger, enforcer),
		connect.WithInterceptors(interceptor),
	)
	mux.Handle(orgPath, orgHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	authClient := authv1connect.NewAuthServiceClient(server.Client(), server.URL)
	orgClient := authv1connect.NewOrganizationServiceClient(server.Client(), server.URL)
	login := func(username string) string {
		t.Helper()
		resp, loginErr := authClient.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
			Username: username, Password: "password",
		}))
		require.NoError(t, loginErr)
		return resp.Msg.GetAccessToken()
	}
	adminToken := login("admin")
	viewerToken := login("alice")

	updateOrg := connect.NewRequest(&authv1.UpdateOrganizationRequest{
		OrgId: "org-1", Name: "Renamed", ExpectedVersion: 0,
	})
	updateOrg.Header().Set("Authorization", "Bearer "+viewerToken)
	_, err = orgClient.UpdateOrganization(ctx, updateOrg)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	updateRole := connect.NewRequest(&authv1.UpdateMemberRoleRequest{
		OrgId: "org-1", UserId: "user-1", NewRole: "release_admin", ExpectedVersion: 0,
	})
	updateRole.Header().Set("Authorization", "Bearer "+adminToken)
	_, err = orgClient.UpdateMemberRole(ctx, updateRole)
	require.NoError(t, err)

	updateOrg = connect.NewRequest(&authv1.UpdateOrganizationRequest{
		OrgId: "org-1", Name: "Renamed", ExpectedVersion: 0,
	})
	updateOrg.Header().Set("Authorization", "Bearer "+viewerToken)
	_, err = orgClient.UpdateOrganization(ctx, updateOrg)
	require.NoError(t, err)
}
