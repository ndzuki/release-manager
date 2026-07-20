package auth

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
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
func TestAuthService_LoginErrorsAreUniform(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
	}{
		{name: "unknown user", username: "missing-user", password: "correct-password"},
		{name: "wrong password", username: "alice", password: "wrong-password"},
		{name: "disabled user", username: "disabled", password: "correct-password"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAuthStore(t)
			ctx := context.Background()
			if tt.username != "missing-user" {
				status := store.UserActive
				if tt.username == "disabled" {
					status = store.UserDisabled
				}
				createAuthUser(t, st, tt.username, "correct-password", status)
			}

			svc := newAuthService(st)
			_, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
				Username: tt.username,
				Password: tt.password,
			}))

			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid credentials")
		})
	}
}

func TestAuthService_LoginPersistsSessionAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "auth.db")
	ctx := context.Background()
	jwt := NewJWTManager([]byte("test-signing-key"), time.Hour, time.Hour)

	st, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	createAuthUser(t, st, "alice", "correct-password", store.UserActive)
	svc := NewAuthService(st, jwt, NewRateLimiter(10, time.Minute), slog.New(slog.DiscardHandler))
	loginResp, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "alice",
		Password: "correct-password",
	}))
	require.NoError(t, err)
	refreshToken := loginResp.Msg.GetRefreshToken()
	require.NoError(t, st.Close())

	st, err = sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	svc = NewAuthService(st, jwt, NewRateLimiter(10, time.Minute), slog.New(slog.DiscardHandler))

	refreshResp, err := svc.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: refreshToken,
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, refreshResp.Msg.GetAccessToken())
	assert.NotEmpty(t, refreshResp.Msg.GetRefreshToken())
}

func TestAuthService_RefreshReplayRevokesTokenFamily(t *testing.T) {
	st := openAuthStore(t)
	ctx := context.Background()
	createAuthUser(t, st, "alice", "correct-password", store.UserActive)
	svc := newAuthService(st)

	loginResp, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "alice",
		Password: "correct-password",
	}))
	require.NoError(t, err)
	oldRefresh := loginResp.Msg.GetRefreshToken()

	rotatedResp, err := svc.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: oldRefresh,
	}))
	require.NoError(t, err)
	newRefresh := rotatedResp.Msg.GetRefreshToken()

	_, err = svc.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: oldRefresh,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	newSession, err := st.AuthSessions().GetByRefreshHash(ctx, svc.jwt.HashRefreshToken(newRefresh))
	require.NoError(t, err)
	assert.True(t, newSession.Revoked, "replaying an old token must revoke the whole family")
}

func TestAuthService_LogoutRevokesAuthenticatedUserSessions(t *testing.T) {
	st := openAuthStore(t)
	ctx := context.Background()
	createAuthUser(t, st, "alice", "correct-password", store.UserActive)
	svc := newAuthService(st)

	firstLogin, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "alice",
		Password: "correct-password",
	}))
	require.NoError(t, err)
	secondLogin, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "alice",
		Password: "correct-password",
	}))
	require.NoError(t, err)

	userCtx := context.WithValue(ctx, userIDKey, "alice-id")
	_, err = svc.Logout(userCtx, connect.NewRequest(&authv1.LogoutRequest{}))
	require.NoError(t, err)

	for _, refresh := range []string{firstLogin.Msg.GetRefreshToken(), secondLogin.Msg.GetRefreshToken()} {
		_, err = svc.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{RefreshToken: refresh}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	}
}

func TestAuthService_LogoutWithRefreshTokenIsIdempotent(t *testing.T) {
	st := openAuthStore(t)
	ctx := context.Background()
	createAuthUser(t, st, "alice", "correct-password", store.UserActive)
	svc := newAuthService(st)

	loginResp, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "alice",
		Password: "correct-password",
	}))
	require.NoError(t, err)
	refresh := loginResp.Msg.GetRefreshToken()

	_, err = svc.Logout(ctx, connect.NewRequest(&authv1.LogoutRequest{RefreshToken: refresh}))
	require.NoError(t, err)
	_, err = svc.Logout(ctx, connect.NewRequest(&authv1.LogoutRequest{RefreshToken: refresh}))
	require.NoError(t, err)
}

func TestAuthService_ChangePasswordRevokesSessions(t *testing.T) {
	st := openAuthStore(t)
	ctx := context.Background()
	createAuthUser(t, st, "alice", "old-password", store.UserActive)
	svc := newAuthService(st)

	loginResp, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "alice",
		Password: "old-password",
	}))
	require.NoError(t, err)

	userCtx := context.WithValue(ctx, userIDKey, "alice-id")
	_, err = svc.ChangePassword(userCtx, connect.NewRequest(&authv1.ChangePasswordRequest{
		OldPassword: "old-password",
		NewPassword: "new-password",
	}))
	require.NoError(t, err)

	_, err = svc.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: loginResp.Msg.GetRefreshToken(),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	newLogin, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "alice",
		Password: "new-password",
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, newLogin.Msg.GetAccessToken())
}

func TestAuthService_DisabledUserCannotRefreshSession(t *testing.T) {
	st := openAuthStore(t)
	ctx := context.Background()
	user := createAuthUser(t, st, "alice", "correct-password", store.UserActive)
	svc := newAuthService(st)

	loginResp, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "alice",
		Password: "correct-password",
	}))
	require.NoError(t, err)

	user.Status = store.UserDisabled
	require.NoError(t, st.Users().Update(ctx, user))

	_, err = svc.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: loginResp.Msg.GetRefreshToken(),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	session, err := st.AuthSessions().GetByRefreshHash(ctx, svc.jwt.HashRefreshToken(loginResp.Msg.GetRefreshToken()))
	require.NoError(t, err)
	assert.True(t, session.Revoked)
}

func TestAuthService_ValidateTokenReturnsPrincipal(t *testing.T) {
	st := openAuthStore(t)
	ctx := context.Background()
	createAuthUser(t, st, "alice", "correct-password", store.UserActive)
	svc := newAuthService(st)

	loginResp, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "alice",
		Password: "correct-password",
	}))
	require.NoError(t, err)

	validated, err := svc.ValidateToken(ctx, connect.NewRequest(&authv1.ValidateTokenRequest{
		Token: loginResp.Msg.GetAccessToken(),
	}))
	require.NoError(t, err)
	assert.True(t, validated.Msg.GetValid())
	assert.Equal(t, "alice-id", validated.Msg.GetUserId())
}

func TestAuthInterceptor_RejectsAccessTokenAfterSessionRevocation(t *testing.T) {
	tests := []struct {
		name   string
		revoke func(*testing.T, context.Context, *sqlitestore.Store, *AuthService)
	}{
		{
			name: "logout",
			revoke: func(t *testing.T, ctx context.Context, _ *sqlitestore.Store, svc *AuthService) {
				t.Helper()
				_, err := svc.Logout(context.WithValue(ctx, userIDKey, "alice-id"), connect.NewRequest(&authv1.LogoutRequest{}))
				require.NoError(t, err)
			},
		},
		{
			name: "disabled user",
			revoke: func(t *testing.T, ctx context.Context, st *sqlitestore.Store, _ *AuthService) {
				t.Helper()
				user, err := st.Users().Get(ctx, "alice-id")
				require.NoError(t, err)
				user.Status = store.UserDisabled
				require.NoError(t, st.Users().Update(ctx, user))
			},
		},
		{
			name: "password change",
			revoke: func(t *testing.T, ctx context.Context, _ *sqlitestore.Store, svc *AuthService) {
				t.Helper()
				_, err := svc.ChangePassword(context.WithValue(ctx, userIDKey, "alice-id"), connect.NewRequest(&authv1.ChangePasswordRequest{
					OldPassword: "correct-password",
					NewPassword: "new-password",
				}))
				require.NoError(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAuthStore(t)
			ctx := context.Background()
			createAuthUser(t, st, "alice", "correct-password", store.UserActive)
			// Seed RBAC: the user must pass Casbin enforcement to reach session validation.
			require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-1", Name: "Org 1"}))
			require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
				OrgID: "org-1", UserID: "alice-id", Role: store.RolePlatformAdmin,
			}))
			enf, err := NewEnforcer(st, slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			require.NoError(t, enf.LoadPolicies(ctx))

			svc := newAuthService(st)
			login, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
				Username: "alice",
				Password: "correct-password",
			}))
			require.NoError(t, err)

			tt.revoke(t, ctx, st, svc)

			interceptor := NewAuthInterceptor(svc.jwt, st, enf, map[string]bool{}, slog.New(slog.DiscardHandler))
			req := connect.NewRequest(&authv1.ValidateTokenRequest{})
			req.Header().Set("Authorization", "Bearer "+login.Msg.GetAccessToken())
			_, err = interceptor(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
				return connect.NewResponse(&authv1.ValidateTokenResponse{Valid: true}), nil
			})(ctx, req)

			require.Error(t, err)
			assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		})
	}
}

func TestStartSessionCleanupDeletesExpiredSessionsAndStops(t *testing.T) {
	st := openAuthStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	createAuthUser(t, st, "alice", "correct-password", store.UserActive)
	require.NoError(t, st.AuthSessions().Create(ctx, &store.AuthSession{
		ID:               uuid.NewString(),
		UserID:           "alice-id",
		TokenFamily:      "family-1",
		RefreshTokenHash: "hash-1",
		ExpiresAt:        time.Now().UTC().Add(-time.Minute),
	}))

	StartSessionCleanup(ctx, st.AuthSessions(), 5*time.Millisecond, slog.New(slog.DiscardHandler))
	assert.Eventually(t, func() bool {
		_, err := st.AuthSessions().GetByRefreshHash(ctx, "hash-1")
		return errors.Is(err, store.ErrNotFound)
	}, time.Second, 5*time.Millisecond)

	cancel()
}

func openAuthStore(t *testing.T) *sqlitestore.Store {
	t.Helper()
	st, err := sqlitestore.Open(filepath.Join(t.TempDir(), "auth.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

func newAuthService(st *sqlitestore.Store) *AuthService {
	return NewAuthService(
		st,
		NewJWTManager([]byte("test-signing-key"), time.Hour, time.Hour),
		NewRateLimiter(10, time.Minute),
		slog.New(slog.DiscardHandler),
	)
}

func createAuthUser(t *testing.T, st *sqlitestore.Store, username, password string, status store.UserStatus) *store.User {
	t.Helper()
	hash, err := HashPassword(password)
	require.NoError(t, err)
	user := &store.User{
		ID:           username + "-id",
		Username:     username,
		PasswordHash: hash,
		Status:       status,
	}
	require.NoError(t, st.Users().Create(context.Background(), user))
	return user
}
