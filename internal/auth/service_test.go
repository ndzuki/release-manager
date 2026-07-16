package auth

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

func TestAuthService_LoginAndRefreshPreserveOrganization(t *testing.T) {
	_, st := setupEnforcer(t)
	ctx := context.Background()
	passwordHash, err := HashPassword("password")
	require.NoError(t, err)
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-1", Name: "Org 1"}))
	require.NoError(t, st.Users().Create(ctx, &store.User{
		ID: "user-1", Username: "alice", PasswordHash: passwordHash,
	}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-1", UserID: "user-1", Role: store.RoleViewer,
	}))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	jwtManager := NewJWTManager([]byte("test-signing-key"), time.Hour, time.Hour)
	service := NewAuthService(st, jwtManager, NewRateLimiter(10, time.Minute), logger)

	login, err := service.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "alice", Password: "password",
	}))
	require.NoError(t, err)
	loginClaims, err := jwtManager.ValidateAccessToken(login.Msg.GetAccessToken())
	require.NoError(t, err)
	assert.Equal(t, "org-1", loginClaims.OrgID)

	refresh, err := service.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: login.Msg.GetRefreshToken(),
	}))
	require.NoError(t, err)
	refreshClaims, err := jwtManager.ValidateAccessToken(refresh.Msg.GetAccessToken())
	require.NoError(t, err)
	assert.Equal(t, "org-1", refreshClaims.OrgID)
}
