//go:build integration

package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	schemamigrations "github.com/ndzuki/release-manager/migrations"
)

func TestRunMigratesCurrentSQLiteSchemaEndToEnd(t *testing.T) {
	baseDSN := os.Getenv("POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	targetDSN := createMigrationSchema(ctx, t, baseDSN)
	sourcePath := createMigrationSource(ctx, t)
	before := fileHash(t, sourcePath)

	reportJSON, err := Run(ctx, Config{SourceDSN: sourcePath, TargetDSN: targetDSN, MigrationFS: schemamigrations.FS})
	require.NoError(t, err)
	assert.Equal(t, before, fileHash(t, sourcePath), "read-only migration must not modify the SQLite source")

	var report Report
	require.NoError(t, json.Unmarshal(reportJSON, &report))
	sourceDB, err := openSQLiteReadOnly(ctx, sourcePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sourceDB.Close()) })
	tables, err := discoverTables(ctx, sourceDB)
	require.NoError(t, err)
	assert.Equal(t, len(tables), report.TablesCopied)
	assert.NotZero(t, report.TotalRows)

	targetDB, err := sql.Open("pgx", targetDSN)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, targetDB.Close()) })

	var stateVersion int64
	var createdByUserID string
	require.NoError(t, targetDB.QueryRowContext(ctx,
		`SELECT state_version, created_by_user_id FROM values_revisions WHERE id = 'values-legacy'`,
	).Scan(&stateVersion, &createdByUserID))
	assert.EqualValues(t, 3, stateVersion)
	assert.Equal(t, "creator-legacy", createdByUserID)

	var candidateDerived, candidateCreated time.Time
	require.NoError(t, targetDB.QueryRowContext(ctx,
		`SELECT last_seen_at, created_at FROM candidate_artifacts WHERE id = 'candidate-migrate'`,
	).Scan(&candidateDerived, &candidateCreated))
	assert.True(t, candidateDerived.Equal(candidateCreated))

	var preflightUpdated, preflightCreated time.Time
	require.NoError(t, targetDB.QueryRowContext(ctx,
		`SELECT updated_at, created_at FROM preflight_lifecycles WHERE id = 'preflight-migrate'`,
	).Scan(&preflightUpdated, &preflightCreated))
	assert.True(t, preflightUpdated.Equal(preflightCreated))

	var linked int
	require.NoError(t, targetDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM bundle_candidate_artifacts WHERE bundle_id = 'bundle-migrate' AND candidate_artifact_id = 'candidate-migrate'`,
	).Scan(&linked))
	assert.Equal(t, 1, linked)
}

func TestRunFailurePreservesSQLiteRollbackSource(t *testing.T) {
	baseDSN := os.Getenv("POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	targetDSN := createMigrationSchema(ctx, t, baseDSN)
	sourcePath := createMigrationSource(ctx, t)

	sourceDB, err := sql.Open("sqlite", sourcePath)
	require.NoError(t, err)
	_, err = sourceDB.ExecContext(ctx, `CREATE TABLE unsupported_source_table (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = sourceDB.ExecContext(ctx, `INSERT INTO unsupported_source_table (id) VALUES ('unsupported')`)
	require.NoError(t, err)
	require.NoError(t, sourceDB.Close())
	before := fileHash(t, sourcePath)

	_, err = Run(ctx, Config{SourceDSN: sourcePath, TargetDSN: targetDSN, MigrationFS: schemamigrations.FS})
	require.ErrorContains(t, err, "PostgreSQL schema is missing source tables")
	assert.Equal(t, before, fileHash(t, sourcePath), "failed migration must preserve the SQLite rollback snapshot")

	targetDB, err := sql.Open("pgx", targetDSN)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, targetDB.Close()) })
	var customerCount int
	require.NoError(t, targetDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM customers`).Scan(&customerCount))
	assert.Zero(t, customerCount, "validation failure must not import partial business data")

	rollbackStore, err := sqlitestore.Open(sourcePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rollbackStore.Close()) })
	require.NoError(t, rollbackStore.Customers().Create(ctx, &store.Customer{
		ID: "customer-after-rollback", Name: "Rollback Customer", Slug: "rollback-customer",
	}))
	_, err = rollbackStore.Customers().Get(ctx, "customer-after-rollback")
	require.NoError(t, err)
}

func createMigrationSchema(ctx context.Context, t *testing.T, baseDSN string) string {
	t.Helper()
	schema := "task070_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	baseDB, err := sql.Open("pgx", baseDSN)
	require.NoError(t, err)
	defer func() { require.NoError(t, baseDB.Close()) }()
	_, err = baseDB.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, schema)) //nolint:gosec // schema is generated from a UUID.
	require.NoError(t, err)

	parsed, err := url.Parse(baseDSN)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func createMigrationSource(ctx context.Context, t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/orchestrator.db"
	dsn := path + "?_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	require.NoError(t, db.PingContext(ctx))
	require.NoError(t, db.Close())
	st, err := sqlitestore.Open(path) //nolint:contextcheck // SQLite Store.Open owns its migration context; this fixture already verified the DSN with ctx.
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: "customer-migrate", Name: "Migration Customer", Slug: "migration-customer"}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: "cluster-migrate", Name: "Migration Cluster", CustomerID: "customer-migrate"}))
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "definition-migrate", Name: "Migration Definition", CustomerID: "customer-migrate", ClusterID: "cluster-migrate",
		Namespace: "default", ReleaseName: "migration-release", Status: store.DefStatusActive,
	}, nil))
	require.NoError(t, st.Values().Create(ctx, &store.ValuesRevision{
		ID: "values-legacy", ReleaseDefinitionID: "definition-migrate", Version: 1, StateVersion: 3,
		Status: store.ValuesStatusDraft, CanonicalDocument: []byte(`{"replicas":2}`), Digest: "sha256:values", CreatedByUserID: "creator-legacy",
	}))
	_, err = st.DB().ExecContext(ctx, `UPDATE values_revisions SET state_version = 0, created_by_user_id = '' WHERE id = 'values-legacy'`)
	require.NoError(t, err)

	require.NoError(t, st.Bundles().Create(ctx, &store.ReleaseBundle{
		ID: "bundle-migrate", Name: "Migration Bundle", DigestAlg: "sha256", DigestValue: "bundle-digest",
		Status: store.BundleValidated, Images: []store.BundleImage{{Ref: "registry/app:v1", Digest: "sha256:image"}},
	}))
	bundleID := "bundle-migrate"
	require.NoError(t, st.CandidateArtifacts().Create(ctx, &store.CandidateArtifact{
		ID: "candidate-migrate", ArtifactType: store.ArtifactImage, Ref: "registry/app:v1", Digest: "sha256:candidate", BundleID: &bundleID,
	}))
	require.NoError(t, st.PreflightLifecycles().CreateOrReset(ctx, "operation-migrate"))
	require.NoError(t, st.PreflightLifecycles().UpdateResult(ctx, "operation-migrate", "passed", "verify"))

	require.NoError(t, st.Users().Create(ctx, &store.User{ID: "user-migrate", Username: "migration-user", PasswordHash: "hash"}))
	require.NoError(t, st.AuthSessions().Create(ctx, &store.AuthSession{
		ID: "auth-session-migrate", UserID: "user-migrate", TokenFamily: "family-migrate", RefreshTokenHash: "refresh-migrate",
		ExpiresAt: now.Add(time.Hour),
	}))
	require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
		ID: "audit-migrate", ActorKind: store.AuditActorUser, ActorID: "user-migrate", ResourceType: "migration",
		ResourceID: "source", Action: "seed", Status: "succeeded", Metadata: map[string]string{"source": "sqlite"},
	}))
	require.NoError(t, st.Verifications().Create(ctx, &store.VerificationRecord{
		ID: "verification-migrate", ArtifactDigest: "sha256:candidate", PolicyVersion: "policy-v1", Status: store.VerificationTrusted,
		RootID: "root-migrate", KeyID: "key-migrate", RevocationEpoch: 2, Issuer: "issuer", Subject: "subject",
	}))
	require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
		ID: "root-migrate", Environment: "staging", KeyID: "key-migrate", PublicKeyPEM: "pem", State: store.TrustRootActive, ValidFrom: now,
	}))
	require.NoError(t, st.ScanResults().Create(ctx, &store.ScanResultRecord{
		ID: "scan-migrate", ArtifactDigest: "sha256:candidate", Scanner: "trivy", ResultVersion: "v1",
		SeverityJSON: []byte(`{"critical":0}`), FindingsJSON: []byte(`[]`), ScannedAt: now,
	}))
	require.NoError(t, st.VulnerabilityExceptions().Create(ctx, &store.VulnerabilityExceptionRecord{
		ID: "exception-migrate", FindingID: "CVE-TEST", ArtifactDigest: "sha256:candidate", Actor: "platform-admin",
		Reason: "migration fixture", ExpiresAt: now.Add(time.Hour),
	}))
	_, err = st.DB().ExecContext(ctx, `INSERT INTO audit_outbox (id, event_type, payload_json, created_at, delivered) VALUES (?, ?, ?, ?, ?)`,
		"audit-outbox-migrate", "MigrationEvent", []byte(`{"revision_id":"values-legacy"}`), now.Format(time.RFC3339Nano), 0)
	require.NoError(t, err)
	_, err = st.DB().ExecContext(ctx, `INSERT INTO notification_outbox (id, event_type, payload_json, created_at, delivered) VALUES (?, ?, ?, ?, ?)`,
		"notification-outbox-migrate", "MigrationEvent", []byte(`{"revision_id":"values-legacy"}`), now.Format(time.RFC3339Nano), 0)
	require.NoError(t, err)

	require.NoError(t, st.Close())
	return path
}

func fileHash(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return sha256.Sum256(contents)
}
