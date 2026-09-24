package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// D1 (2026-09-28): an existing database that still carries the cache-based
// preflight_results table must lose it on the next Open.
//
// That is the whole reason the DROP is *appended* to migrationStatements instead
// of the CREATE being deleted: removing the CREATE would make fresh databases
// clean while leaving every pre-existing database with the table forever.
func TestLegacyMigrationDropsPreflightResults(t *testing.T) {
	legacyPath := t.TempDir() + "/legacy.db"
	raw, err := sql.Open("sqlite", legacyPath)
	require.NoError(t, err)
	// Simulate a pre-D1 database: the table exists before Open runs migrations.
	_, err = raw.ExecContext(context.Background(), `CREATE TABLE preflight_results (
		id TEXT PRIMARY KEY,
		operation_id TEXT NOT NULL,
		routing_version TEXT NOT NULL DEFAULT '',
		bundle_digest TEXT NOT NULL,
		trust_policy_version TEXT NOT NULL DEFAULT '',
		sbom_policy_version TEXT NOT NULL DEFAULT '',
		result_json BLOB NOT NULL,
		created_at TEXT NOT NULL
	)`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	st, err := Open(legacyPath)
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	var tables int
	require.NoError(t, st.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'preflight_results'`).Scan(&tables))
	require.Zero(t, tables, "the legacy preflight_results table must be dropped by the migration")

	// The table next to it (REQ-019 preflight_lifecycles) is a different feature
	// and must survive: D1 drops exactly one table.
	var lifecycles int
	require.NoError(t, st.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'preflight_lifecycles'`).Scan(&lifecycles))
	require.Equal(t, 1, lifecycles, "preflight_lifecycles belongs to another feature and must not be touched")
}
