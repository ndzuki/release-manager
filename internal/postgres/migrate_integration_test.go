//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRunMigrationsUpAndNoChange(t *testing.T) {
	baseDSN := os.Getenv("POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dsn := migrationTestSchema(ctx, t, baseDSN)
	database, err := Open(ctx, config.DatabaseConfig{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	migrationFS, err := LoadMigrationFS("../../migrations")
	require.NoError(t, err)
	require.NoError(t, RunMigrations(ctx, database.SQLDB(), migrationFS))
	require.NoError(t, RunMigrations(ctx, database.SQLDB(), migrationFS))
	require.NoError(t, RunMigrationsDown(ctx, database.SQLDB(), migrationFS, 1))
	var version int
	var dirty bool
	require.NoError(t, database.SQLDB().QueryRowContext(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty))
	require.Equal(t, 6, version)
	require.False(t, dirty)
	require.NoError(t, RunMigrations(ctx, database.SQLDB(), migrationFS))
	require.NoError(t, database.SQLDB().QueryRowContext(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty))
	require.Equal(t, 7, version)
	require.False(t, dirty)

	var nonTZCount int
	err = database.SQLDB().QueryRowContext(ctx, `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND (column_name LIKE '%_at' OR column_name IN ('deadline', 'last_heartbeat', 'expires_at'))
  AND data_type <> 'timestamp with time zone'
`).Scan(&nonTZCount)
	require.NoError(t, err)
	require.Zero(t, nonTZCount)
}

func migrationTestSchema(ctx context.Context, t *testing.T, baseDSN string) string {
	t.Helper()
	schema := "task070_migrate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
