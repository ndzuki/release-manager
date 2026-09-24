//go:build integration

package postgres_test

import (
	"database/sql"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/postgres"
)

// D1 (2026-09-28): the full migration set must leave no preflight_results table
// on PostgreSQL either (migrations/000031_drop_preflight_results.up.sql), while
// preflight_lifecycles — a different, live feature — survives untouched.
func TestMigrationsDropPreflightResults(t *testing.T) {
	baseDSN := os.Getenv("POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	dsn := postgresStoreTestSchema(ctx, t, baseDSN)

	database, err := postgres.Open(ctx, config.DatabaseConfig{DSN: dsn})
	require.NoError(t, err)
	migrationFS, err := postgres.LoadMigrationFS("../../../migrations")
	require.NoError(t, err)
	require.NoError(t, postgres.RunMigrations(ctx, database.SQLDB(), migrationFS))

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck // test-local handle; the schema cleanup drops everything.

	var count int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables
		 WHERE table_schema = current_schema() AND table_name = 'preflight_results'`).Scan(&count))
	require.Zero(t, count, "migration 000031 must drop preflight_results")

	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables
		 WHERE table_schema = current_schema() AND table_name = 'preflight_lifecycles'`).Scan(&count))
	require.Equal(t, 1, count, "preflight_lifecycles belongs to another feature and must not be touched")
}
