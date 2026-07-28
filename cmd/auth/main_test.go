package main

import (
	"context"
	"database/sql"
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
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
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

func TestAuthPostgreSQLSessionPersistsAcrossRestart(t *testing.T) {
	baseDSN := os.Getenv("POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	dsn := authTestSchema(ctx, t, baseDSN)
	logger := slog.New(slog.DiscardHandler)
	const signingKey = "auth-postgresql-signing-key"

	first := &authSvc{signingKey: signingKey}
	first.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "postgres", DSN: dsn}, Maintenance: true})
	firstMux := http.NewServeMux()
	require.NoError(t, first.Register(firstMux, logger))
	assert.IsType(t, &postgresstore.Store{}, first.store)
	user := &store.User{ID: uuid.NewString(), Username: "postgres-auth-user", PasswordHash: "unused"}
	require.NoError(t, first.store.Users().Create(ctx, user))
	session := &store.AuthSession{ID: uuid.NewString(), UserID: user.ID, TokenFamily: uuid.NewString(), RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	require.NoError(t, first.store.AuthSessions().Create(ctx, session))
	require.NoError(t, first.Close())

	second := &authSvc{signingKey: signingKey}
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
