package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	redisstore "github.com/ndzuki/release-manager/internal/store/redis"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestAuthReadOnlyProcedures(t *testing.T) {
	readOnly := authReadOnlyProcedures()
	for _, procedure := range []string{
		authv1connect.AuthServiceGetInitStatusProcedure,
		authv1connect.AuthServiceValidateTokenProcedure,
		authv1connect.OrganizationServiceGetOrganizationProcedure,
		authv1connect.OrganizationServiceListOrganizationsProcedure,
		authv1connect.OrganizationServiceListMembersProcedure,
		authv1connect.BindingServiceGetBindingProcedure,
		authv1connect.BindingServiceListBindingsProcedure,
		authv1connect.ExternalIdentityServiceGetOIDCAuthURLProcedure,
		authv1connect.ExternalIdentityServiceGetDingTalkAuthURLProcedure,
	} {
		assert.Contains(t, readOnly, procedure)
	}
	for _, procedure := range []string{
		authv1connect.AuthServiceInitializeProcedure,
		authv1connect.AuthServiceLoginProcedure,
		authv1connect.AuthServiceRefreshTokenProcedure,
		authv1connect.AuthServiceChangePasswordProcedure,
		authv1connect.OrganizationServiceCreateOrganizationProcedure,
		authv1connect.BindingServiceRevokeBindingProcedure,
		authv1connect.ExternalIdentityServiceAuthenticateLDAPProcedure,
	} {
		assert.NotContains(t, readOnly, procedure)
	}
}

// authTestJWTPrivateKeyPEM is the Ed25519 signing key every cmd/auth test
// registers with (REQ-065 AC-065-01): Register parses PEM key material, so the
// tests need a real key rather than a placeholder string.
var authTestJWTPrivateKeyPEM = func() string {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("generate test JWT key: " + err.Error())
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		panic("marshal test JWT key: " + err.Error())
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}()

func TestAuthPostgreSQLSessionPersistsAcrossRestart(t *testing.T) {
	baseDSN := os.Getenv("POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	dsn := authTestSchema(ctx, t, baseDSN)
	logger := slog.New(slog.DiscardHandler)

	first := &authSvc{jwtPrivateKey: authTestJWTPrivateKeyPEM}
	first.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "postgres", DSN: dsn}, Maintenance: true})
	firstMux := http.NewServeMux()
	require.NoError(t, first.Register(firstMux, logger))
	assert.IsType(t, &postgresstore.Store{}, first.store)
	user := &store.User{ID: uuid.NewString(), Username: "postgres-auth-user", PasswordHash: "unused"}
	require.NoError(t, first.store.Users().Create(ctx, user))
	session := &store.AuthSession{ID: uuid.NewString(), UserID: user.ID, TokenFamily: uuid.NewString(), RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	require.NoError(t, first.store.AuthSessions().Create(ctx, session))
	require.NoError(t, first.Close())

	second := &authSvc{jwtPrivateKey: authTestJWTPrivateKeyPEM}
	second.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "postgres", DSN: dsn}, Maintenance: true})
	secondMux := http.NewServeMux()
	require.NoError(t, second.Register(secondMux, logger))
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	got, err := second.store.AuthSessions().Get(ctx, session.ID)
	require.NoError(t, err)
	assert.Equal(t, session.TokenFamily, got.TokenFamily)

	server := httptest.NewServer(secondMux)
	t.Cleanup(server.Close)
	client := authv1connect.NewAuthServiceClient(server.Client(), server.URL)
	_, err = client.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{Username: "blocked", Password: "blocked", OrganizationName: "blocked"}))
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	status, err := client.GetInitStatus(ctx, connect.NewRequest(&authv1.GetInitStatusRequest{}))
	require.NoError(t, err)
	assert.True(t, status.Msg.GetInitialized())
}

// REQ-065 AC-065-01: cmd/auth is the only service holding the Ed25519 signing
// key, and a missing or non-Ed25519 key must fail startup. The regression this
// pins is the previous dev contract: dev-up minted 64 random bytes, base64
// encoded, as the HS256 secret. That material must no longer configure a signing
// manager — otherwise the AC would pass on paper while the process still ran on
// a symmetric key.
func TestAuthRegisterFailsClosedOnNonEd25519Key(t *testing.T) {
	legacy := make([]byte, 64)
	_, err := rand.Read(legacy)
	require.NoError(t, err)

	svc := &authSvc{jwtPrivateKey: base64.StdEncoding.EncodeToString(legacy)}
	svc.Configure(&config.ServiceConfig{
		Database: config.DatabaseConfig{Driver: "sqlite", DSN: t.TempDir() + "/auth.db"},
	})

	err = svc.Register(http.NewServeMux(), slog.New(slog.DiscardHandler))
	require.Error(t, err, "the legacy symmetric key format must not configure a signing manager")
	assert.Contains(t, err.Error(), "jwt signing key")
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
}

func TestAuthDatabaseOnlySessionLifecycle(t *testing.T) {
	svc := &authSvc{jwtPrivateKey: authTestJWTPrivateKeyPEM}
	svc.Configure(&config.ServiceConfig{
		Database: config.DatabaseConfig{Driver: "sqlite", DSN: t.TempDir() + "/auth.db"},
		Redis:    config.RedisConfig{},
	})
	mux := http.NewServeMux()
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	assert.IsType(t, &sqlitestore.Store{}, svc.store)
	assert.NotContains(t, svc.ReadinessChecks(), "redis")

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := authv1connect.NewAuthServiceClient(server.Client(), server.URL)
	ctx := t.Context()
	initialized, err := client.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{
		Username: "database-only-admin", Password: "password", OrganizationName: "Database Only Org",
	}))
	require.NoError(t, err)
	rotated, err := client.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{RefreshToken: initialized.Msg.GetRefreshToken()}))
	require.NoError(t, err)
	assert.NotEmpty(t, rotated.Msg.GetRefreshToken())

	request := connect.NewRequest(&authv1.LogoutRequest{RefreshToken: rotated.Msg.GetRefreshToken()})
	request.Header().Set("Authorization", "Bearer "+rotated.Msg.GetAccessToken())
	_, err = client.Logout(ctx, request)
	require.NoError(t, err)
}

func TestAuthRedisSessionAdapterLifecycle(t *testing.T) {
	mini := miniredis.RunT(t)
	dbPath := t.TempDir() + "/auth.db"
	svc := &authSvc{jwtPrivateKey: authTestJWTPrivateKeyPEM}
	svc.Configure(&config.ServiceConfig{
		Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath},
		Redis:    config.RedisConfig{Address: mini.Addr()},
	})
	mux := http.NewServeMux()
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	assert.IsType(t, &sessionStore{}, svc.store)
	assert.IsType(t, &redisstore.Adapter{}, svc.store.AuthSessions())
	checks := svc.ReadinessChecks()
	require.Contains(t, checks, "redis")
	require.NoError(t, checks["redis"]())

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := authv1connect.NewAuthServiceClient(server.Client(), server.URL)
	ctx := t.Context()
	initialized, err := client.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{
		Username:         "redis-admin",
		Password:         "password",
		OrganizationName: "Redis Org",
	}))
	require.NoError(t, err)
	oldRefresh := initialized.Msg.GetRefreshToken()
	rotated, err := client.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{RefreshToken: oldRefresh}))
	require.NoError(t, err)
	_, err = client.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{RefreshToken: oldRefresh}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	_, err = client.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{RefreshToken: rotated.Msg.GetRefreshToken()}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	mini.Close()
	_, err = client.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: "redis-admin", Password: "password"}))
	assertVerificationUnavailable(t, err)
	_, err = client.RefreshToken(ctx, connect.NewRequest(&authv1.RefreshTokenRequest{RefreshToken: oldRefresh}))
	assertVerificationUnavailable(t, err)

	require.Error(t, checks["redis"]())

	require.NoError(t, mini.Restart())
	require.Eventually(t, func() bool { return checks["redis"]() == nil }, time.Second, 10*time.Millisecond)
	_, err = client.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: "redis-admin", Password: "password"}))
	require.NoError(t, err)

	redisClient := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	assert.NotEmpty(t, mini.Keys())
}

func assertVerificationUnavailable(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "verification_unavailable")
}

func authTestSchema(ctx context.Context, t *testing.T, baseDSN string) string {
	t.Helper()
	schema := "task070_auth_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	db, err := sql.Open("pgx", baseDSN)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, schema)) //nolint:gosec // schema is generated from a UUID.
	require.NoError(t, err)
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, dropErr := db.ExecContext(cleanupCtx, fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema)) //nolint:gosec // schema is generated from a UUID.
		require.NoError(t, dropErr)
		require.NoError(t, db.Close())
	})
	parsed, err := url.Parse(baseDSN)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
