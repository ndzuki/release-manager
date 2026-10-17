package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// stubDecision allows every audit request and resolves the effective scope to the
// caller's own organization, which is what release-auth answers for a
// release_admin of that organization (ADR-021). The release-api test injects it
// instead of standing up release-auth.
type stubDecision struct {
	organization  string
	maxWindowDays int32
}

func (s stubDecision) Authorize(
	_ context.Context, _, _, _, _ string,
) (audit.AccessDecision, error) {
	return audit.AccessDecision{
		Allowed: true, Reason: "ok", OrganizationID: s.organization,
		MaxWindowDays: s.maxWindowDays, PolicyVersion: 3,
	}, nil
}

// apiTestJWTKeys is the Ed25519 management-plane key pair every cmd/api test
// uses (REQ-065 AC-065-01): the audit API is configured with the public half
// and the tests mint tokens with the private half.
var (
	apiTestJWTPublicKeyPEM string
	apiTestJWTPrivateKey   ed25519.PrivateKey
)

func init() {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("generate test JWT key pair: " + err.Error())
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		panic("marshal test JWT public key: " + err.Error())
	}
	apiTestJWTPublicKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	apiTestJWTPrivateKey = privateKey
}

func TestAPISvcAuditConnectEndToEnd(t *testing.T) {
	dbPath := t.TempDir() + "/api.db"
	mux := http.NewServeMux()
	// A short flush interval keeps the audit wait deterministic: the production
	// default is 5s, which leaves under a second of slack in the Eventually budget.
	svc := &apiSvc{
		dbPath: dbPath, jwtPublicKey: apiTestJWTPublicKeyPEM, auditFlushInterval: 10 * time.Millisecond,
		decisionClient: stubDecision{organization: "org-001", maxWindowDays: 31},
	}
	require.NoError(t, svc.Register(mux, slog.Default()))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jwtManager := auth.NewJWTManager(apiTestJWTPrivateKey, time.Hour, 24*time.Hour)
	token, _, err := jwtManager.GenerateAccessToken(
		"user-001",
		"org-001",
		[]string{string(store.RoleReleaseAdmin)},
	)
	require.NoError(t, err)
	client := auditv1connect.NewAuditServiceClient(http.DefaultClient, server.URL)
	now := time.Now().UTC()

	emitRequest := connect.NewRequest(&auditv1.EmitAuditRequest{Events: []*auditv1.AuditEvent{{
		Id: "event-001",
		Actor: &auditv1.AuditActor{
			Kind:           auditv1.ActorKind_ACTOR_KIND_USER,
			Id:             "user-001",
			OrganizationId: "org-001",
			Role:           string(store.RoleReleaseAdmin),
		},
		ResourceType: "release_operation",
		ResourceId:   "operation-001",
		Action:       "create",
		Status:       "accepted",
		CreatedAt:    timestamppb.New(now.Add(-time.Minute)),
	}}})
	emitRequest.Header().Set("Authorization", "Bearer "+token)
	emitResponse, err := client.Emit(context.Background(), emitRequest)
	require.NoError(t, err)
	assert.Equal(t, int32(1), emitResponse.Msg.GetAccepted())

	require.Eventually(t, func() bool {
		queryRequest := connect.NewRequest(&auditv1.QueryAuditEventsRequest{
			Filter: &auditv1.AuditQueryFilter{
				TimeRange: &commonv1.TimestampRange{
					Start: timestamppb.New(now.Add(-time.Hour)),
					End:   timestamppb.New(now),
				},
			},
		})
		queryRequest.Header().Set("Authorization", "Bearer "+token)
		queryResponse, queryErr := client.QueryAuditEvents(context.Background(), queryRequest)
		return queryErr == nil && len(queryResponse.Msg.GetEvents()) == 1
	}, 6*time.Second, 100*time.Millisecond)

	exportRequest := connect.NewRequest(&auditv1.ExportAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{
			TimeRange: &commonv1.TimestampRange{
				Start: timestamppb.New(now.Add(-time.Hour)),
				End:   timestamppb.New(now),
			},
		},
	})
	exportRequest.Header().Set("Authorization", "Bearer "+token)
	exportResponse, err := client.ExportAuditEvents(context.Background(), exportRequest)
	require.NoError(t, err)
	assert.NotEmpty(t, exportResponse.Msg.GetExportId())
	assert.Equal(t, "pending", exportResponse.Msg.GetStatus())
}

// REQ-065 AC-065-01: cmd/api verifies EdDSA tokens with a public key, and a
// missing or non-Ed25519 key must fail startup rather than leave the service
// accepting nothing (or, worse, something it cannot check).
func TestAPIRegisterFailsClosedOnNonEd25519Key(t *testing.T) {
	legacy := make([]byte, 64)
	_, err := rand.Read(legacy)
	require.NoError(t, err)

	svc := &apiSvc{dbPath: t.TempDir() + "/api.db", jwtPublicKey: base64.StdEncoding.EncodeToString(legacy)}

	err = svc.Register(http.NewServeMux(), slog.New(slog.DiscardHandler))
	require.Error(t, err, "the legacy symmetric key format must not configure a verifier")
	assert.Contains(t, err.Error(), "jwt verification key")
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
}

func TestAPISvcCloseDrainsAuditEmitter(t *testing.T) {
	dbPath := t.TempDir() + "/api.db"
	mux := http.NewServeMux()
	svc := &apiSvc{dbPath: dbPath, jwtPublicKey: apiTestJWTPublicKeyPEM}
	require.NoError(t, svc.Register(mux, slog.Default()))

	result := svc.emitter.Emit(&store.AuditEvent{
		ID:             "event-close",
		ActorKind:      store.AuditActorSystem,
		OrganizationID: "org-001",
		ResourceType:   "api_service",
		ResourceID:     "release-api",
		Action:         "close",
		Status:         "accepted",
		Metadata:       map[string]string{},
	})
	require.True(t, result.Accepted)
	require.NoError(t, svc.Close())

	st, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	event, err := st.AuditEvents().GetByID(context.Background(), "event-close")
	require.NoError(t, err)
	assert.Equal(t, "org-001", event.OrganizationID)
}

// TASK-338 AC-338-02: with no database.driver the engine stays SQLite, --db
// remains the file the audit surface persists to, and the audit query contract
// reads the event back from that same file.
func TestRegisterDefaultsToSQLiteAuditPath(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "")
	t.Setenv("DATABASE_DSN", "")
	dbPath := filepath.Join(t.TempDir(), "api.db")

	svc := &apiSvc{dbPath: dbPath, jwtPublicKey: apiTestJWTPublicKeyPEM}
	require.NoError(t, svc.Register(http.NewServeMux(), slog.New(slog.DiscardHandler)))
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	_, isSQLite := svc.store.(*sqlitestore.Store)
	require.True(t, isSQLite, "an unset database.driver must keep the SQLite default")

	result := svc.emitter.Emit(&store.AuditEvent{
		ID:             "event-default-engine",
		ActorKind:      store.AuditActorSystem,
		OrganizationID: "org-default",
		ResourceType:   "api_service",
		ResourceID:     "release-api",
		Action:         "query",
		Status:         "accepted",
		Metadata:       map[string]string{},
	})
	require.True(t, result.Accepted)
	require.NoError(t, svc.Close())

	// The --db file, not a config-declared path, received the event.
	require.FileExists(t, dbPath)
	st, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	page, err := st.AuditEvents().Query(context.Background(), store.AuditEventFilter{OrganizationID: "org-default"}, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	assert.Equal(t, "event-default-engine", page.Events[0].ID)
}

// AC-338-02 failure path: an explicit SQLite driver without a DSN keeps --db
// authoritative instead of failing validation.
func TestOpenStoreSQLiteDriverFallsBackToDBPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "explicit.db")
	st, err := openStore(dbPath, config.DatabaseConfig{Driver: "sqlite"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	_, isSQLite := st.(*sqlitestore.Store)
	require.True(t, isSQLite)
	require.FileExists(t, dbPath)
}

// AC-338-02 failure path: an unknown or incomplete engine selection is rejected
// before any store is opened, rather than silently falling back to SQLite.
func TestOpenStoreRejectsInvalidDriverConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.DatabaseConfig
		want string
	}{
		{
			name: "unknown driver",
			cfg:  config.DatabaseConfig{Driver: "mysql"},
			want: "database.driver must be postgres or sqlite",
		},
		{
			name: "postgres without dsn",
			cfg:  config.DatabaseConfig{Driver: "postgres"},
			want: "dsn_invalid",
		},
		{
			name: "postgres with sqlite dsn",
			cfg:  config.DatabaseConfig{Driver: "postgres", DSN: "data/api.db"},
			want: "dsn_invalid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := openStore(filepath.Join(t.TempDir(), "api.db"), tt.cfg)
			require.ErrorContains(t, err, tt.want)
			require.Nil(t, st)
		})
	}
}

// TASK-338: the engine selection must be assertable without a database. Both
// constructors are replaced with recorders, so this pins which one openStore
// dispatches to and with which DSN; the real PostgreSQL constructor dials and
// migrates a server and cannot otherwise be observed. A driver branch mutated to
// always take SQLite makes the postgres case fail here instead of only in CI.
func TestOpenStoreSelectsConstructorByDriver(t *testing.T) {
	const pgDSN = "postgres://release_manager:s3cret@postgres:5432/release_manager?sslmode=disable"
	dbPath := filepath.Join(t.TempDir(), "api.db")

	tests := []struct {
		name       string
		cfg        config.DatabaseConfig
		wantEngine string
		wantDSN    string
		wantErr    string
	}{
		{
			name:       "postgres driver reaches the postgres constructor",
			cfg:        config.DatabaseConfig{Driver: "postgres", DSN: pgDSN},
			wantEngine: "postgres",
			wantDSN:    pgDSN,
		},
		{
			name:       "unset driver keeps the sqlite default",
			cfg:        config.DatabaseConfig{},
			wantEngine: "sqlite",
			wantDSN:    dbPath,
		},
		{
			name:       "explicit sqlite without dsn falls back to db path",
			cfg:        config.DatabaseConfig{Driver: "sqlite"},
			wantEngine: "sqlite",
			wantDSN:    dbPath,
		},
		{
			name:    "invalid driver is rejected before any constructor",
			cfg:     config.DatabaseConfig{Driver: "mysql"},
			wantErr: "database.driver must be postgres or sqlite",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var engine, gotDSN string
			ctors := storeConstructors{
				sqlite: func(dsn string) (store.Store, error) {
					engine, gotDSN = "sqlite", dsn
					return nil, nil
				},
				postgres: func(_ context.Context, cfg config.DatabaseConfig, _ fs.FS) (store.Store, error) {
					engine, gotDSN = "postgres", cfg.DSN
					return nil, nil
				},
			}

			opened, err := openStoreWith(ctors, dbPath, tt.cfg)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, opened)
				require.Empty(t, engine, "an invalid driver must not reach a constructor")
				return
			}
			require.NoError(t, err)
			require.Nil(t, opened, "the recorder constructors return no store")
			assert.Equal(t, tt.wantEngine, engine)
			assert.Equal(t, tt.wantDSN, gotDSN)
		})
	}
}

// A PostgreSQL DSN embeds the password, so the opened-store log must name the
// engine only; the SQLite DSN is a plain file path and stays loggable.
func TestStoreLogAttrsNeverLeakPostgresDSN(t *testing.T) {
	const dsn = "postgres://release_manager:s3cret@postgres:5432/release_manager?sslmode=disable"
	attrs := storeLogAttrs(config.DatabaseConfig{Driver: "postgres", DSN: dsn}, "unused.db")
	assert.Equal(t, []any{"driver", "postgres"}, attrs)
	assert.NotContains(t, fmt.Sprint(attrs...), "s3cret")

	sqliteAttrs := storeLogAttrs(config.DatabaseConfig{}, "/data/api.db")
	assert.Equal(t, []any{"driver", "sqlite", "db", "/data/api.db"}, sqliteAttrs)
}
