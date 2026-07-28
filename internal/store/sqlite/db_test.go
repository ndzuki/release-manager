package sqlite

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestMigratePreflightLifecyclesLegacySchema(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/legacy.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := t.Context()

	_, err = db.ExecContext(ctx, `
		CREATE TABLE preflight_lifecycles (
			id TEXT PRIMARY KEY,
			operation_id TEXT,
			operation_terminal_at TEXT,
			stages TEXT NOT NULL DEFAULT '[]',
			overall TEXT NOT NULL DEFAULT '',
			error_code TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		);
		INSERT INTO preflight_lifecycles VALUES
			('duplicate-old', 'operation-1', NULL, '[{"stage":"artifact","status":"passed"}]', 'passed', '', '2026-07-01T00:00:00Z'),
			('duplicate-new', 'operation-1', '2026-07-03T00:00:00Z', '[{"stage":"artifact","status":"passed"},{"stage":"render","status":"passed"}]', 'timeout', '', '2026-07-02T00:00:00Z'),
			('exploratory', NULL, NULL, '[]', 'passed', '', '2026-07-01T00:00:00Z');
	`)
	require.NoError(t, err)
	require.NoError(t, migratePreflightLifecycles(db))

	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM preflight_lifecycles`).Scan(&count))
	assert.Equal(t, 2, count)
	var id, stages, overall, createdAt, updatedAt string
	var terminalAt sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT id, operation_terminal_at, stages, overall, created_at, updated_at
		FROM preflight_lifecycles WHERE operation_id = 'operation-1'
	`).Scan(&id, &terminalAt, &stages, &overall, &createdAt, &updatedAt))
	require.True(t, terminalAt.Valid)
	assert.Equal(t, "duplicate-new", id)
	assert.Equal(t, "2026-07-03T00:00:00Z", terminalAt.String)
	assert.Equal(t, "artifact,render", stages)
	assert.Equal(t, "cancelled", overall)
	assert.Equal(t, createdAt, updatedAt)

	_, err = db.ExecContext(ctx, `
		INSERT INTO preflight_lifecycles (id, operation_id, stages, overall, created_at, updated_at)
		VALUES ('duplicate-third', 'operation-1', '', 'running', '2026-07-04T00:00:00Z', '2026-07-04T00:00:00Z')
	`)
	require.Error(t, err)
}
