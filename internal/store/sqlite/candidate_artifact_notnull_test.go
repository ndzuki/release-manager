package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// downgradeCandidateArtifactsNullable turns a migrated database back into the
// pre-TASK-235 shape: candidate_artifacts exists, last_seen_at is nullable. The current
// code cannot produce that shape, so the regression test creates it explicitly.
//
// The downgrade itself needs the same two precautions the production rebuild documents:
// legacy_alter_table keeps the rename from rewriting bundle_candidate_artifacts' FK
// target, and the copy names its columns so nothing depends on column order.
func downgradeCandidateArtifactsNullable(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	for _, statement := range []string{
		`PRAGMA legacy_alter_table = ON`,
		`ALTER TABLE candidate_artifacts RENAME TO candidate_artifacts_current`,
		`CREATE TABLE candidate_artifacts (
			id            TEXT PRIMARY KEY,
			artifact_type TEXT NOT NULL CHECK (artifact_type IN ('image','chart')),
			ref           TEXT NOT NULL,
			digest        TEXT NOT NULL,
			bundle_id     TEXT,
			created_at    TEXT NOT NULL,
			validated_at  TEXT,
			source_id     TEXT NOT NULL DEFAULT '',
			orphaned_at   TEXT,
			last_seen_at  TEXT,
			UNIQUE(digest, artifact_type)
		)`,
		`INSERT INTO candidate_artifacts (
			id, artifact_type, ref, digest, bundle_id, created_at,
			validated_at, source_id, orphaned_at, last_seen_at
		) SELECT id, artifact_type, ref, digest, bundle_id, created_at,
			validated_at, source_id, orphaned_at, last_seen_at
		  FROM candidate_artifacts_current`,
		`DROP TABLE candidate_artifacts_current`,
		`PRAGMA legacy_alter_table = OFF`,
	} {
		_, err := db.ExecContext(context.Background(), statement)
		require.NoError(t, err)
	}
}

// TASK-235: candidate_artifacts.last_seen_at must be NOT NULL on SQLite too, matching
// PostgreSQL. A legacy database carries it as nullable, and adding the constraint in
// place is impossible, so the migration rebuilds the table. The rebuild must not lose
// parent rows, must repair NULLs from created_at, and -- the hazard that shapes the
// implementation -- must not cascade-delete bundle_candidate_artifacts link rows while it
// renames and drops the old parent.
func TestCandidateArtifactLastSeenAtRebuildPreservesRowsAndLinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	ctx := context.Background()
	created, err := sqlitestore.Open(path)
	require.NoError(t, err)
	// The link rows below reference real bundles, so the fixture stays FK-clean and the
	// rebuild's copy runs under the same enforcement production uses.
	for _, id := range []string{"bundle-1", "bundle-2"} {
		require.NoError(t, created.Bundles().Create(ctx, &store.ReleaseBundle{
			ID: id, Name: id, DigestAlg: "sha256", DigestValue: id, Status: store.BundleValidated,
		}))
	}
	require.NoError(t, created.Close())
	downgradeCandidateArtifactsNullable(t, path)

	seen := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	nullArtifactID := "artifact-null"
	maintainedID := "artifact-maintained"

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	for _, statement := range []string{
		`INSERT INTO candidate_artifacts (id, artifact_type, ref, digest, created_at, last_seen_at)
		 VALUES ('` + nullArtifactID + `', 'image', 'registry/api:1', 'sha256:null', '` + seen.Format(time.RFC3339Nano) + `', NULL)`,
		`INSERT INTO candidate_artifacts (id, artifact_type, ref, digest, created_at, last_seen_at)
		 VALUES ('` + maintainedID + `', 'chart', 'charts/api:1', 'sha256:kept', '` + seen.Format(time.RFC3339Nano) + `', '` + seen.Add(time.Hour).Format(time.RFC3339Nano) + `')`,
		`INSERT INTO bundle_candidate_artifacts (bundle_id, artifact_id, linked_at) VALUES
			('bundle-1', '` + nullArtifactID + `', '` + seen.Format(time.RFC3339Nano) + `'),
			('bundle-1', '` + maintainedID + `', '` + seen.Format(time.RFC3339Nano) + `'),
			('bundle-2', '` + nullArtifactID + `', '` + seen.Format(time.RFC3339Nano) + `')`,
	} {
		_, err := db.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
	// The fixture itself is checked before the migration runs: the ①/③ assertions below
	// only mean something if the seeded rows were really there.
	var seededArtifacts, seededLinks int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM candidate_artifacts`).Scan(&seededArtifacts))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bundle_candidate_artifacts`).Scan(&seededLinks))
	require.Equal(t, 2, seededArtifacts, "fixture: parent rows")
	require.Equal(t, 3, seededLinks, "fixture: link rows")
	require.NoError(t, db.Close())

	migrated, err := sqlitestore.Open(path)
	require.NoError(t, err)

	// ① the parent rows and their other columns survive unchanged.
	var artifacts int
	require.NoError(t, migrated.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM candidate_artifacts`).Scan(&artifacts))
	assert.Equal(t, 2, artifacts, "the rebuild must copy every parent row")
	var ref, digest string
	require.NoError(t, migrated.DB().QueryRowContext(ctx,
		`SELECT ref, digest FROM candidate_artifacts WHERE id = ?`, nullArtifactID).Scan(&ref, &digest))
	assert.Equal(t, "registry/api:1", ref)
	assert.Equal(t, "sha256:null", digest)

	// ② no NULL survives, and the repaired row took created_at.
	var nulls int
	require.NoError(t, migrated.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM candidate_artifacts WHERE last_seen_at IS NULL`).Scan(&nulls))
	assert.Zero(t, nulls, "the rebuild must COALESCE the legacy NULL from created_at")
	var repairedAt string
	require.NoError(t, migrated.DB().QueryRowContext(ctx,
		`SELECT last_seen_at FROM candidate_artifacts WHERE id = ?`, nullArtifactID).Scan(&repairedAt))
	assert.Equal(t, seen.Format(time.RFC3339Nano), repairedAt)
	repaired, err := migrated.CandidateArtifacts().Get(ctx, nullArtifactID)
	require.NoError(t, err)
	assert.WithinDuration(t, seen, repaired.LastSeenAt, time.Second)

	// ③ both link rows of the repaired artifact and the third link survive: the rebuild
	// must not cascade-delete them while the old parent table is renamed and dropped.
	var links int
	require.NoError(t, migrated.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM bundle_candidate_artifacts`).Scan(&links))
	assert.Equal(t, 3, links, "the parent rebuild must not delete bundle_candidate_artifacts rows")
	var nullArtifactLinks int
	require.NoError(t, migrated.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM bundle_candidate_artifacts WHERE artifact_id = ?`, nullArtifactID).Scan(&nullArtifactLinks))
	assert.Equal(t, 2, nullArtifactLinks)

	// ④ the column is NOT NULL now.
	var notNull int
	require.NoError(t, migrated.DB().QueryRowContext(ctx,
		`SELECT "notnull" FROM pragma_table_info('candidate_artifacts') WHERE name = 'last_seen_at'`).Scan(&notNull))
	assert.Equal(t, 1, notNull, "last_seen_at must be NOT NULL after the rebuild")
	require.NoError(t, migrated.Close())

	// ⑤ reopening is idempotent: the guard sees NOT NULL and does nothing.
	again, err := sqlitestore.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, again.Close()) }()
	require.NoError(t, again.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM candidate_artifacts`).Scan(&artifacts))
	assert.Equal(t, 2, artifacts)
	require.NoError(t, again.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM bundle_candidate_artifacts`).Scan(&links))
	assert.Equal(t, 3, links)
	require.NoError(t, again.DB().QueryRowContext(ctx,
		`SELECT "notnull" FROM pragma_table_info('candidate_artifacts') WHERE name = 'last_seen_at'`).Scan(&notNull))
	assert.Equal(t, 1, notNull)
}

// A fresh database declares last_seen_at NOT NULL in CREATE TABLE and skips the rebuild;
// the column and its constraint must match what the legacy path produces.
func TestFreshCandidateArtifactsDeclareLastSeenAtNotNull(t *testing.T) {
	st, err := sqlitestore.Open(filepath.Join(t.TempDir(), "fresh.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()

	var notNull int
	require.NoError(t, st.DB().QueryRowContext(context.Background(),
		`SELECT "notnull" FROM pragma_table_info('candidate_artifacts') WHERE name = 'last_seen_at'`).Scan(&notNull))
	assert.Equal(t, 1, notNull)

	_, err = st.DB().ExecContext(context.Background(),
		`INSERT INTO candidate_artifacts (id, artifact_type, ref, digest, created_at) VALUES ('x', 'image', 'r', 'd', '2026-10-01T00:00:00Z')`)
	assert.Error(t, err, "a fresh database must reject a NULL last_seen_at")
}
