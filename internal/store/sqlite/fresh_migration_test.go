package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenFreshSchemaMatchesLegacyMigration locks the TASK-084 fast-path
// invariant: a freshly opened database (page-level template clone) carries
// exactly the same schema objects as a database migrated by the legacy loop.
// The legacy reference is forced by pre-creating a throwaway schema object,
// which routes Open into migrateLegacy; the throwaway table is then excluded
// from the comparison.
func TestOpenFreshSchemaMatchesLegacyMigration(t *testing.T) {
	legacyPath := t.TempDir() + "/legacy.db"
	raw, err := sql.Open("sqlite", legacyPath)
	require.NoError(t, err)
	// Any schema object routes the migration into the legacy repair loop.
	_, err = raw.ExecContext(context.Background(), "CREATE TABLE throwaway_marker (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	legacy, err := Open(legacyPath)
	require.NoError(t, err)
	t.Cleanup(func() { legacy.Close() })

	clone, err := Open(t.TempDir() + "/fresh.db")
	require.NoError(t, err)
	t.Cleanup(func() { clone.Close() })

	legacySQL := readMasterSQL(t, legacy.db)
	cloneSQL := readMasterSQL(t, clone.db)
	require.NotEmpty(t, legacySQL, "legacy migration must produce schema objects")
	for name, sqlText := range legacySQL {
		if name == "throwaway_marker" {
			// The marker is the test's own artifact; the fresh clone is a
			// different database and must not inherit it.
			continue
		}
		got, ok := cloneSQL[name]
		require.True(t, ok, "fresh clone is missing schema object %q", name)
		require.Equal(t, sqlText, got, "schema object %q differs between legacy migration and fresh clone", name)
	}
	// The marker must stay untouched (legacy loop is additive for non-empty DBs).
	require.NotContains(t, cloneSQL, "throwaway_marker", "fresh clone must not inherit legacy-path artifacts")
}

// TestOpenFreshSeedRows verifies the data-level migration statements
// (INSERT OR IGNORE) are replayed on the fast path: both seeded rows must be
// present exactly once after a fresh open.
func TestOpenFreshSeedRows(t *testing.T) {
	st, err := Open("file:fresh-seed-rows?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	for table, want := range map[string]int{"authorization_source_version": 1, "policy_version": 1} {
		var n int
		err := st.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&n)
		require.NoError(t, err)
		require.Equal(t, want, n, "seed row for %s must be present exactly once", table)
	}
}

// TestMigrateFreshDDLReplay exercises the DDL-snapshot fallback (used when the
// backup API is unavailable) directly and asserts the replayed schema matches
// the legacy outcome.
func TestMigrateFreshDDLReplay(t *testing.T) {
	ddl, seed, ok := freshSchema()
	require.True(t, ok, "fresh schema snapshot must be buildable")

	db, err := sql.Open("sqlite", "file:ddl-replay?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, migrateFresh(db, ddl, seed))

	legacy, err := Open(t.TempDir() + "/legacy.db") // may take any path; schema identical
	require.NoError(t, err)
	t.Cleanup(func() { legacy.Close() })

	replayed := readMasterSQL(t, db)
	legacySQL := readMasterSQL(t, legacy.db)
	for name, sqlText := range legacySQL {
		require.Equal(t, sqlText, replayed[name], "DDL-replayed object %q differs from legacy outcome", name)
	}
}

// TestOpenPrivateMemoryDSNFallsBackToDDL verifies that a non-shared in-memory
// DSN (where a second connection cannot address the same database) still
// yields a migrated store instead of an empty one.
func TestOpenPrivateMemoryDSNFallsBackToDDL(t *testing.T) {
	st, err := Open("file:private-memory?mode=memory")
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	var n int
	err = st.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE name = 'release_definitions'").Scan(&n)
	require.NoError(t, err)
	require.Equal(t, 1, n, "private memory DSN must still receive the full schema")
}

func readMasterSQL(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := db.QueryContext(context.Background(),
		`SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var name, sqlText string
		require.NoError(t, rows.Scan(&name, &sqlText))
		out[name] = strings.TrimSpace(sqlText)
	}
	require.NoError(t, rows.Err())
	return out
}
