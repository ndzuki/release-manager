//go:build integration

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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	"github.com/ndzuki/release-manager/migrations"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TASK-338 AC-338-01/03: with DATABASE_DRIVER=postgres the audit RPC reads the
// events auth/orchestrator write into PostgreSQL, which the SQLite default can
// never see. The row is inserted through a second store connection (the
// orchestrator's view of the same database) and must come back through the real
// HTTP query path: mux -> JWT interceptor -> AuditService -> store.AuditEvents.
func TestAPISvcAuditReadsPostgresEvents(t *testing.T) {
	baseDSN := os.Getenv("POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	dsn := apiPostgresTestSchema(t, baseDSN)
	t.Setenv("DATABASE_DRIVER", "postgres")
	t.Setenv("DATABASE_DSN", dsn)

	// The service config carries no database section: the engine selection must
	// arrive through the documented DATABASE_DRIVER/DATABASE_DSN binding.
	cfgPath := filepath.Join(t.TempDir(), "api.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("http_port: 8088\n"), 0o600))

	mux := http.NewServeMux()
	svc := &apiSvc{
		dbPath:             filepath.Join(t.TempDir(), "unused.db"),
		jwtPublicKey:       apiTestJWTPublicKeyPEM,
		configPath:         cfgPath,
		auditFlushInterval: 10 * time.Millisecond,
		decisionClient:     stubDecision{organization: "org-pg", maxWindowDays: 31},
	}
	require.NoError(t, svc.Register(mux, slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	_, isPostgres := svc.store.(*postgresstore.Store)
	require.True(t, isPostgres, "DATABASE_DRIVER=postgres must select the PostgreSQL store")
	require.NoFileExists(t, svc.dbPath, "the SQLite file must not be created when PostgreSQL is selected")

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	writer, err := postgresstore.Open(context.Background(), config.DatabaseConfig{
		Driver: "postgres", DSN: dsn,
	}, migrations.FS)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Close()) })

	now := time.Now().UTC()
	eventID := "event-pg-338-" + uuid.NewString()
	require.NoError(t, writer.AuditEvents().CreateBatch(context.Background(), []*store.AuditEvent{{
		ID:             eventID,
		ActorKind:      store.AuditActorSystem,
		ActorID:        "orchestrator",
		OrganizationID: "org-pg",
		ResourceType:   "release_operation",
		ResourceID:     "operation-pg",
		Action:         "create",
		Status:         "accepted",
		Metadata:       map[string]string{"source": "task-338"},
		CreatedAt:      now.Add(-time.Minute),
	}}))

	jwtManager := auth.NewJWTManager(apiTestJWTPrivateKey, time.Hour, 24*time.Hour)
	token, _, err := jwtManager.GenerateAccessToken(
		"user-pg",
		"org-pg",
		[]string{string(store.RoleReleaseAdmin)},
	)
	require.NoError(t, err)
	client := auditv1connect.NewAuditServiceClient(http.DefaultClient, server.URL)

	queryRequest := connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{
			ResourceId: "operation-pg",
			TimeRange: &commonv1.TimestampRange{
				Start: timestamppb.New(now.Add(-time.Hour)),
				End:   timestamppb.New(now),
			},
		},
	})
	queryRequest.Header().Set("Authorization", "Bearer "+token)
	response, err := client.QueryAuditEvents(context.Background(), queryRequest)
	require.NoError(t, err)
	require.Len(t, response.Msg.GetEvents(), 1,
		"the event written on the orchestrator connection must be visible to /audit")
	assert.Equal(t, eventID, response.Msg.GetEvents()[0].GetId())
}

// apiPostgresTestSchema creates a per-test schema the way
// internal/store/postgres/store_test.go does, so the integration run never
// touches the production release_manager schema, and drops it afterwards.
func apiPostgresTestSchema(t *testing.T, baseDSN string) string {
	t.Helper()
	schema := "task338_api_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	db, err := sql.Open("pgx", baseDSN)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), fmt.Sprintf(`CREATE SCHEMA %q`, schema)) //nolint:gosec // schema is generated from a UUID.
	require.NoError(t, err)
	cleanupCtx := context.WithoutCancel(t.Context())
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
