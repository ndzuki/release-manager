package auth_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/auth"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// TestAuthService_BrowserSessionDualMode pins ADR-028: with the browser branch
// enabled, Login must keep returning the bearer pair (so devseed / the E2E
// runner / the kulala collections keep working) *and* hand the console its
// cookies, and ValidateToken must accept either carrier.
//
// Before ADR-028 the service ran token-only: Login answered 200 with no
// Set-Cookie and no user, so the cookie-only console was stuck on /login (B1).
func TestAuthService_BrowserSessionDualMode(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	st, err := sqlitestore.Open("file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	service := auth.NewAuthService(
		st,
		auth.NewJWTManager(privateKey, 15*time.Minute, 24*time.Hour),
		auth.NewRateLimiter(100, time.Minute),
		slog.Default(),
		nil,
		auth.BrowserSessionConfig{SecureCookies: false},
	)
	ctx := context.Background()

	_, err = service.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{
		Username:         "admin",
		Password:         "correct horse battery staple",
		OrganizationName: "Platform",
	}))
	require.NoError(t, err)

	login, err := service.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "admin",
		Password: "correct horse battery staple",
	}))
	require.NoError(t, err)

	// Browser half of dual mode: the three cookies the console depends on.
	cookies := map[string]string{}
	for _, cookie := range login.Header().Values("Set-Cookie") {
		name, value, found := strings.Cut(cookie, "=")
		require.True(t, found, "malformed Set-Cookie: %s", cookie)
		cookies[name] = value
	}
	assert.Contains(t, cookies, auth.AccessCookieName)
	assert.Contains(t, cookies, auth.RefreshCookieName)
	assert.Contains(t, cookies, auth.CSRFCookieName)

	// Token half of dual mode: devseed reads AccessToken from this very response
	// (internal/devfixture/runner.go), so an empty value breaks `make dev-seed`.
	assert.NotNil(t, login.Msg.GetUser())
	assert.Equal(t, "admin", login.Msg.GetUser().GetUsername())
	assert.NotEmpty(t, login.Msg.GetAccessToken())
	assert.NotEmpty(t, login.Msg.GetRefreshToken())

	// ValidateToken accepts both carriers, selected by the request itself.
	viaToken, err := service.ValidateToken(ctx, connect.NewRequest(&authv1.ValidateTokenRequest{
		Token: login.Msg.GetAccessToken(),
	}))
	require.NoError(t, err)
	assert.True(t, viaToken.Msg.GetValid())

	cookieReq := connect.NewRequest(&authv1.ValidateTokenRequest{})
	cookieReq.Header().Set("Cookie", auth.AccessCookieName+"="+login.Msg.GetAccessToken())
	viaCookie, err := service.ValidateToken(ctx, cookieReq)
	require.NoError(t, err)
	assert.True(t, viaCookie.Msg.GetValid())

	// A request that carries neither carrier stays unauthenticated.
	_, err = service.ValidateToken(ctx, connect.NewRequest(&authv1.ValidateTokenRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestAuthService_TokenModeUnaffected pins the other half of ADR-028: a service
// built without a BrowserSessionConfig (the token-only shape other tests and
// any pre-ADR-028 deployment still use) must keep answering ValidateToken from
// the body token and must not set cookies.
func TestAuthService_TokenModeUnaffected(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	st, err := sqlitestore.Open("file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	service := auth.NewAuthService(
		st,
		auth.NewJWTManager(privateKey, 15*time.Minute, 24*time.Hour),
		auth.NewRateLimiter(100, time.Minute),
		slog.Default(),
		nil,
	)
	ctx := context.Background()

	_, err = service.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{
		Username:         "admin",
		Password:         "correct horse battery staple",
		OrganizationName: "Platform",
	}))
	require.NoError(t, err)

	login, err := service.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: "admin",
		Password: "correct horse battery staple",
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, login.Msg.GetAccessToken())
	assert.Empty(t, login.Header().Values("Set-Cookie"))

	valid, err := service.ValidateToken(ctx, connect.NewRequest(&authv1.ValidateTokenRequest{
		Token: login.Msg.GetAccessToken(),
	}))
	require.NoError(t, err)
	assert.True(t, valid.Msg.GetValid())
}

// TestAuthService_BrowserCallerGetsNoTokens pins ADR-028 clause 6: a request
// that comes from a browser page (Origin present) gets the cookies but NOT the
// bearer pair, so an XSS in the console origin cannot read a token out of the
// response body. CLI/CI callers keep the tokens (previous test).
func TestAuthService_BrowserCallerGetsNoTokens(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	st, err := sqlitestore.Open("file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	service := auth.NewAuthService(
		st,
		auth.NewJWTManager(privateKey, 15*time.Minute, 24*time.Hour),
		auth.NewRateLimiter(100, time.Minute),
		slog.Default(),
		nil,
		auth.BrowserSessionConfig{SecureCookies: false},
	)
	ctx := context.Background()

	_, err = service.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{
		Username:         "admin",
		Password:         "correct horse battery staple",
		OrganizationName: "Platform",
	}))
	require.NoError(t, err)

	req := connect.NewRequest(&authv1.LoginRequest{
		Username: "admin",
		Password: "correct horse battery staple",
	})
	req.Header().Set("Origin", "http://127.0.0.1:8087")
	login, err := service.Login(ctx, req)
	require.NoError(t, err)

	// The console still authenticates: it gets its cookies and its principal.
	assert.Len(t, login.Header().Values("Set-Cookie"), 3)
	assert.Equal(t, "admin", login.Msg.GetUser().GetUsername())
	// ...but no token reaches the page.
	assert.Empty(t, login.Msg.GetAccessToken())
	assert.Empty(t, login.Msg.GetRefreshToken())
}
