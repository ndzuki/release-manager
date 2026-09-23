package auth

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// This file closes the REQ-025 acceptance criteria that had no reproducible
// evidence: the behaviours were implemented (and D-11 fixed the rate-limit
// counting rule as "all login attempts, including successful ones") but no test
// pinned them. Each test names the AC it evidences.

// AC-025-05: the sixth login attempt inside the one-minute window is rejected
// with 429 CodeResourceExhausted, and the window slides back open.
//
// D-11 (2026-08-05) settled the counting rule: every login attempt counts,
// including successful ones, and the threshold is 5/min. The limiter is
// therefore driven directly rather than through Login, so the assertion is
// about the sliding window itself and not about credential handling.
func TestRateLimiter_SixthAttemptInWindowIsRejected(t *testing.T) {
	limiter := NewRateLimiter(5, time.Minute)
	const key = "alice"

	for attempt := 1; attempt <= 5; attempt++ {
		assert.True(t, limiter.Allow(key), "attempt %d must be allowed", attempt)
	}
	assert.False(t, limiter.Allow(key), "the 6th attempt in the window must be rejected (AC-025-05)")

	// A different key has its own budget: the window is per account, not global.
	assert.True(t, limiter.Allow("bob"), "another account must not inherit the exhausted window")
}

// The window slides: an entry older than the window stops counting, so the
// account recovers without any explicit reset.
func TestRateLimiter_WindowSlidesBackOpen(t *testing.T) {
	limiter := NewRateLimiter(1, 20*time.Millisecond)
	const key = "alice"

	require.True(t, limiter.Allow(key))
	require.False(t, limiter.Allow(key), "the second attempt inside the window must be rejected")

	time.Sleep(40 * time.Millisecond)
	assert.True(t, limiter.Allow(key), "the attempt must stop counting once the window has passed")
}

// AC-025-06: a second Initialize on an already initialized system is rejected
// with 409 CodeAlreadyExists rather than creating a second organization.
func TestInitialize_SecondCallReturnsAlreadyExists(t *testing.T) {
	enforcer, st := setupEnforcer(t)
	ctx := context.Background()
	svc := NewAuthService(st, NewJWTManager(testJWTPrivateKey, time.Hour, time.Hour),
		NewRateLimiter(10, time.Minute), slog.New(slog.DiscardHandler), enforcer)

	first, err := svc.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{
		Username: "dev-admin", Password: "dev-admin-pass", OrganizationName: "Dev Org",
	}))
	require.NoError(t, err)
	require.NotEmpty(t, first.Msg.GetAccessToken())

	_, err = svc.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{
		Username: "second-admin", Password: "second-admin-pass", OrganizationName: "Second Org",
	}))
	require.Error(t, err, "a second Initialize must be rejected (AC-025-06)")
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

	// The rejection must not have created the second organization.
	orgs, err := st.Organizations().List(ctx)
	require.NoError(t, err)
	assert.Len(t, orgs, 1, "the rejected Initialize must not leave a second organization behind")
}

// AC-025-07: presenting an expired refresh token is rejected with 401 and the
// whole token family is revoked, so the rotation chain cannot be resumed.
//
// This is the expiry path; TestAuthService_RefreshReplayRevokesTokenFamily
// covers the replay path, which is a different branch in
// refreshSessionForRotation.
func TestAuthService_ExpiredRefreshRevokesTokenFamily(t *testing.T) {
	st := openAuthStore(t)
	ctx := context.Background()
	createAuthUser(t, st, "alice", "correct-password", store.UserActive)
	svc := newAuthService(st)

	const rawRefresh = "expired-refresh-token"
	const family = "family-expired"

	require.NoError(t, st.AuthSessions().Create(ctx, &store.AuthSession{
		ID:               "session-expired",
		UserID:           "alice-id",
		TokenFamily:      family,
		RefreshTokenHash: svc.jwt.HashRefreshToken(rawRefresh),
		ExpiresAt:        time.Now().UTC().Add(-time.Minute),
		CreatedAt:        time.Now().UTC().Add(-2 * time.Hour),
	}))

	_, err := svc.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: rawRefresh,
	}))
	require.Error(t, err, "an expired refresh token must be rejected (AC-025-07)")
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	sessions, err := st.AuthSessions().GetByTokenFamily(ctx, family)
	require.NoError(t, err)
	require.NotEmpty(t, sessions, "the expired family must still be recorded")
	for _, session := range sessions {
		assert.True(t, session.Revoked, "expiry must revoke the whole family (AC-025-07)")
	}
}

// AC-025-04: an unknown username, a wrong password and a disabled account all
// produce the same 401 with the same message, so Login cannot be used to
// enumerate accounts. The three paths are separate branches in Login (the first
// two share one), which is why the assertion compares the rendered errors
// rather than only the status code.
func TestAuthService_LoginErrorsDoNotRevealAccountExistence(t *testing.T) {
	st := openAuthStore(t)
	ctx := context.Background()
	createAuthUser(t, st, "alice", "correct-password", store.UserActive)
	createAuthUser(t, st, "carol", "correct-password", store.UserDisabled)
	svc := newAuthService(st)

	cases := []struct {
		name     string
		username string
		password string
	}{
		{name: "unknown username", username: "nobody", password: "correct-password"},
		{name: "wrong password", username: "alice", password: "wrong-password"},
		{name: "disabled account", username: "carol", password: "correct-password"},
	}

	var first string
	for _, tc := range cases {
		_, err := svc.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
			Username: tc.username,
			Password: tc.password,
		}))
		require.Error(t, err, tc.name)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), tc.name)

		if first == "" {
			first = err.Error()
			continue
		}
		assert.Equal(t, first, err.Error(),
			"%s must be indistinguishable from the other failures (AC-025-04)", tc.name)
	}
	assert.Contains(t, first, "invalid credentials")
}
