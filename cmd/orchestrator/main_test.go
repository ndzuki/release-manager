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

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/postgres"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/migrations"
)

func TestOrchestratorValuesApprovalEndToEnd(t *testing.T) {
	const signingKey = "test-signing-key"
	ctx := context.Background()
	dbPath := t.TempDir() + "/orchestrator.db"
	seedStore, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	const (
		organizationID   = "org-068-smoke"
		customerID       = "customer-068-smoke"
		definitionID     = "definition-068-smoke"
		revisionID       = "revision-068-smoke"
		viewerRevisionID = "revision-068-viewer"
		creatorID        = "creator-068-smoke"
		approverID       = "approver-068-smoke"
		viewerID         = "viewer-068-smoke"
	)
	ownerOrganizationID := organizationID
	require.NoError(t, seedStore.Customers().Create(ctx, &store.Customer{
		ID: customerID, Name: "Customer 068 Smoke", Slug: "customer-068-smoke",
	}))
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{
		ID: organizationID, Name: "Organization 068 Smoke",
	}))
	require.NoError(t, seedStore.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-068-smoke", OrgID: organizationID, CustomerID: customerID,
	}))
	for userID, role := range map[string]store.Role{
		creatorID:  store.RoleDeployer,
		approverID: store.RoleReleaseAdmin,
		viewerID:   store.RoleViewer,
	} {
		require.NoError(t, seedStore.Users().Create(ctx, &store.User{
			ID: userID, Username: userID, PasswordHash: "unused",
		}))
		require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{
			OrgID: organizationID, UserID: userID, Role: role,
		}))
		require.NoError(t, seedStore.AuthSessions().Create(ctx, &store.AuthSession{
			ID: uuid.NewString(), UserID: userID, TokenFamily: uuid.NewString(),
			RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour),
		}))
	}
	require.NoError(t, seedStore.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: definitionID, Name: "definition-068-smoke", CustomerID: customerID,
		ClusterID: "cluster-068-smoke", ReleaseName: "release-068-smoke",
		Status: store.DefStatusActive, OwnerOrganizationID: &ownerOrganizationID,
	}, nil))
	require.NoError(t, seedStore.Values().Create(ctx, &store.ValuesRevision{
		ID: revisionID, ReleaseDefinitionID: definitionID, Revision: 1,
		StateVersion: 1, Status: store.ValuesStatusDraft, Values: []byte(`{"replicas":1}`),
		Digest: "sha256:068-smoke", CreatedByUserID: creatorID,
	}))
	require.NoError(t, seedStore.Values().Create(ctx, &store.ValuesRevision{
		ID: viewerRevisionID, ReleaseDefinitionID: definitionID, Revision: 2,
		StateVersion: 1, Status: store.ValuesStatusDraft, Values: []byte(`{"replicas":2}`),
		Digest: "sha256:068-viewer", CreatedByUserID: viewerID,
	}))
	require.NoError(t, seedStore.Close())

	mux := http.NewServeMux()
	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey}
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath}})
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	creatorToken, _, err := jwtManager.GenerateAccessToken(
		creatorID, organizationID, []string{string(store.RoleDeployer)},
	)
	require.NoError(t, err)
	approverToken, _, err := jwtManager.GenerateAccessToken(
		approverID, organizationID, []string{string(store.RoleReleaseAdmin)},
	)
	require.NoError(t, err)
	viewerToken, _, err := jwtManager.GenerateAccessToken(
		viewerID, organizationID, []string{string(store.RoleViewer)},
	)
	require.NoError(t, err)
	client := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, server.URL)
	viewerRequest := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
		RevisionId: viewerRevisionID, ExpectedStateVersion: 1, Comment: "ready",
	})
	viewerRequest.Header().Set("Authorization", "Bearer "+viewerToken)
	viewerRequest.Header().Set("Idempotency-Key", "submit-068-viewer")
	_, err = client.SubmitValuesRevision(ctx, viewerRequest)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.ErrorContains(t, err, "insufficient for submit")
	viewerAudit, err := svc.store.ValuesApprovalEvidence().ListAuditOutbox(ctx, viewerRevisionID)
	require.NoError(t, err)
	require.Len(t, viewerAudit, 1)
	viewerRevision, err := svc.store.Values().Get(ctx, viewerRevisionID)
	require.NoError(t, err)
	assert.Equal(t, store.ValuesStatusDraft, viewerRevision.Status)

	submitRequest := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
		RevisionId: revisionID, ExpectedStateVersion: 1, Comment: "ready",
	})
	submitRequest.Header().Set("Authorization", "Bearer "+creatorToken)
	submitRequest.Header().Set("Idempotency-Key", "submit-068-smoke")
	submitResponse, err := client.SubmitValuesRevision(ctx, submitRequest)
	require.NoErrorf(t, err, "submit values revision failed: code=%s err=%v", connect.CodeOf(err), err)
	assert.Equal(t, int64(2), submitResponse.Msg.GetRevision().GetStateVersion())

	approveRequest := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{
		RevisionId: revisionID, ExpectedStateVersion: 2, Comment: "approved",
	})
	approveRequest.Header().Set("Authorization", "Bearer "+approverToken)
	approveRequest.Header().Set("Idempotency-Key", "approve-068-smoke")
	approveResponse, err := client.ApproveValuesRevision(ctx, approveRequest)
	require.NoErrorf(t, err, "approve values revision failed: code=%s err=%v", connect.CodeOf(err), err)
	assert.Equal(t, int64(3), approveResponse.Msg.GetRevision().GetStateVersion())
	assert.Equal(t, "VALUES_STATUS_APPROVED", approveResponse.Msg.GetNewState().String())

	legacyRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/values-revisions/"+revisionID+"/approve", http.NoBody)
	require.NoError(t, err)
	legacyResponse, err := http.DefaultClient.Do(legacyRequest)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, legacyResponse.Body.Close()) })
	assert.Equal(t, http.StatusNotFound, legacyResponse.StatusCode)
}

func TestOrchestratorPostgreSQLCutoverAuthority(t *testing.T) {
	baseDSN := os.Getenv("POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	dsn := orchestratorTestSchema(ctx, t, baseDSN)
	database, err := postgres.Open(ctx, config.DatabaseConfig{Driver: "postgres", DSN: dsn})
	require.NoError(t, err)
	require.NoError(t, postgres.RunMigrations(ctx, database.SQLDB(), migrations.FS))
	seedStore, err := postgresstore.New(database.SQLDB(), database.GORM())
	require.NoError(t, err)
	const (
		signingKey     = "postgres-cutover-signing-key"
		organizationID = "org-070-cutover"
		userID         = "user-070-cutover"
		customerID     = "customer-070-cutover"
	)
	require.NoError(t, seedStore.Organizations().Create(ctx, &store.Organization{ID: organizationID, Name: "Cutover Org"}))
	require.NoError(t, seedStore.Users().Create(ctx, &store.User{ID: userID, Username: userID, PasswordHash: "unused"}))
	require.NoError(t, seedStore.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: organizationID, UserID: userID, Role: store.RolePlatformAdmin}))
	require.NoError(t, seedStore.AuthSessions().Create(ctx, &store.AuthSession{
		ID: uuid.NewString(), UserID: userID, TokenFamily: uuid.NewString(), RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))
	require.NoError(t, seedStore.Close())

	svc := &orchSvc{targetEnv: "staging", signingKey: signingKey}
	svc.Configure(&config.ServiceConfig{Database: config.DatabaseConfig{Driver: "postgres", DSN: dsn}})
	mux := http.NewServeMux()
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager([]byte(signingKey), time.Hour, time.Hour)
	token, _, err := jwtManager.GenerateAccessToken(userID, organizationID, []string{string(store.RolePlatformAdmin)})
	require.NoError(t, err)
	request := connect.NewRequest(&orchestratorv1.CreateCustomerRequest{Id: customerID, Name: "PostgreSQL Customer", Slug: "postgresql-customer"})
	request.Header().Set("Authorization", "Bearer "+token)
	client := orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL)
	_, err = client.CreateCustomer(ctx, request)
	require.NoError(t, err)
	assert.IsType(t, &postgresstore.Store{}, svc.store)
	created, err := svc.store.Customers().Get(ctx, customerID)
	require.NoError(t, err)
	assert.Equal(t, "PostgreSQL Customer", created.Name)

	sqlitePath := t.TempDir() + "/rollback.db"
	rollback, err := sqlitestore.Open(sqlitePath)
	require.NoError(t, err)
	require.NoError(t, rollback.Customers().Create(ctx, &store.Customer{ID: "sqlite-before-cutover", Name: "SQLite Snapshot", Slug: "sqlite-snapshot"}))
	require.NoError(t, rollback.Close())
	before, err := os.ReadFile(sqlitePath)
	require.NoError(t, err)
	require.NoError(t, svc.store.Customers().Create(ctx, &store.Customer{ID: "postgres-after-cutover", Name: "PostgreSQL Only", Slug: "postgres-only"}))
	after, err := os.ReadFile(sqlitePath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "PostgreSQL writes must not modify the SQLite rollback snapshot")

	rollback, err = sqlitestore.Open(sqlitePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rollback.Close()) })
	_, err = rollback.Customers().Get(ctx, "sqlite-before-cutover")
	require.NoError(t, err)
	_, err = rollback.Customers().Get(ctx, "postgres-after-cutover")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func orchestratorTestSchema(ctx context.Context, t *testing.T, baseDSN string) string {
	t.Helper()
	schema := "task070_orchestrator_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

func TestOrchestratorReadOnlyProcedures(t *testing.T) {
	readOnly := orchestratorReadOnlyProcedures()
	for _, procedure := range []string{
		orchestratorv1connect.OrchestratorServiceGetReleaseDefinitionProcedure,
		orchestratorv1connect.OrchestratorServiceListReleaseDefinitionsProcedure,
		orchestratorv1connect.OrchestratorServiceGetCustomerProcedure,
		orchestratorv1connect.OrchestratorServiceListCustomersProcedure,
		orchestratorv1connect.OrchestratorServiceGetClusterProcedure,
		orchestratorv1connect.OrchestratorServiceListClustersProcedure,
		orchestratorv1connect.OrchestratorServiceGetClusterRoutesProcedure,
	} {
		assert.Contains(t, readOnly, procedure)
	}
	for _, procedure := range []string{
		orchestratorv1connect.OrchestratorServiceCreateOperationProcedure,
		orchestratorv1connect.OrchestratorServiceCreateEnrollmentTokenProcedure,
		orchestratorv1connect.OrchestratorServiceEmergencyChangeProcedure,
		orchestratorv1connect.OrchestratorServiceSyncInventoryProcedure,
		orchestratorv1connect.OrchestratorServiceConfigureClusterRouteProcedure,
	} {
		assert.NotContains(t, readOnly, procedure)
	}
}
