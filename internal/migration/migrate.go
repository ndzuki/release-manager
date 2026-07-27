// Package migration implements a one-shot SQLite-to-PostgreSQL data migration
// with foreign-key-aware copying, time-column conversion, backfill, and validation.
package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx-backed database/sql driver for PostgreSQL
	_ "modernc.org/sqlite"             // pure-Go SQLite driver

	"github.com/ndzuki/release-manager/internal/postgres"
)

// Config holds the migration configuration.
type Config struct {
	SourceDSN   string // SQLite file path, opened read-only
	TargetDSN   string // PostgreSQL connection URL
	MigrationFS fs.FS  // PostgreSQL migration filesystem for pre-migration schema setup
}

// Report is the machine-readable JSON migration result.
// It never includes DSN, connection strings, or other sensitive fields.
type Report struct {
	TablesCopied   int              `json:"tables_copied"`
	TotalRows      int64            `json:"total_rows"`
	Duration       string           `json:"duration"`
	TableRowCounts map[string]int64 `json:"table_row_counts"`
	Backfilled     []string         `json:"backfilled"`
	Errors         []string         `json:"errors,omitempty"`
}

// Run executes the full migration: open source, run target migrations, copy data
// in FK-topological order, backfill, validate, and return a JSON report.
// On any validation failure the PostgreSQL transaction is rolled back and a
// data_import_mismatch error is returned.
// The source SQLite file is opened read-only and never modified.
//
// Error classification: connection errors from the PostgreSQL target carry the
// "connection_unavailable" prefix; migration application failures carry
// "migration_failed"; data integrity failures carry "data_import_mismatch".
// The caller MUST NOT expose the target DSN or any connection string to users.
//
//nolint:gocyclo // The one-shot migration keeps its rollback gates explicit in execution order.
func Run(ctx context.Context, cfg Config) (json.RawMessage, error) {
	start := time.Now()
	sourceDigestBefore, err := fileDigest(cfg.SourceDSN)
	if err != nil {
		return nil, fmt.Errorf("data_import_mismatch: hash source: %w", err)
	}

	// --- Open source SQLite read-only ---
	srcDB, err := openSQLiteReadOnly(ctx, cfg.SourceDSN)
	if err != nil {
		return nil, fmt.Errorf("data_import_mismatch: open source: %w", err)
	}
	defer srcDB.Close()

	// --- Open target PostgreSQL ---
	tgtDB, err := sql.Open("pgx", cfg.TargetDSN)
	if err != nil {
		return nil, fmt.Errorf("connection_unavailable: open target: %w", err)
	}
	defer tgtDB.Close()
	if err := tgtDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connection_unavailable: ping target: %w", err)
	}

	// --- Run PostgreSQL migrations ---
	if err := postgres.RunMigrations(ctx, tgtDB, cfg.MigrationFS); err != nil {
		// Preserve the migration_failed prefix from postgres.RunMigrations.
		return nil, err
	}

	// --- Discover source tables ---
	tables, err := discoverTables(ctx, srcDB)
	if err != nil {
		return nil, fmt.Errorf("data_import_mismatch: discover tables: %w", err)
	}

	// --- Require every source table to have an explicit PostgreSQL target ---
	targetTables, err := targetTableIntersect(ctx, tgtDB, tables)
	if err != nil {
		return nil, fmt.Errorf("connection_unavailable: query target tables: %w", err)
	}
	if missing := missingTables(tables, targetTables); len(missing) > 0 {
		return nil, fmt.Errorf("data_import_mismatch: PostgreSQL schema is missing source tables: %v", missing)
	}
	tables = targetTables

	if len(tables) == 0 {
		return nil, fmt.Errorf("data_import_mismatch: source database contains no migratable tables")
	}

	ordered := orderTables(tables)

	// --- Copy data in a single PostgreSQL transaction ---
	tx, err := tgtDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("connection_unavailable: begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after a successful Commit.

	rowCounts := make(map[string]int64)

	for _, tbl := range ordered {
		cols, timeCols, blobCols, err := readTableColumns(ctx, srcDB, tbl)
		if err != nil {
			return nil, fmt.Errorf("data_import_mismatch: read columns for %s: %w", tbl, err)
		}
		n, err := copyTable(ctx, srcDB, tx, tbl, cols, timeCols, blobCols)
		if err != nil {
			return nil, fmt.Errorf("data_import_mismatch: copy %s: %w", tbl, err)
		}
		rowCounts[tbl] = n
	}

	// --- Run backfills ---
	backfilled, err := runBackfills(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("data_import_mismatch: backfill: %w", err)
	}

	// --- Validate ---
	if err := validateMigration(ctx, srcDB, tx, ordered, rowCounts); err != nil {
		return nil, err // already wrapped with data_import_mismatch
	}

	sourceDigestAfter, err := fileDigest(cfg.SourceDSN)
	if err != nil {
		return nil, fmt.Errorf("data_import_mismatch: rehash source: %w", err)
	}
	if sourceDigestBefore != sourceDigestAfter {
		return nil, fmt.Errorf("data_import_mismatch: SQLite source changed during migration")
	}
	// --- Commit ---
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("connection_unavailable: commit: %w", err)
	}

	// --- Build report ---
	var totalRows int64
	for _, n := range rowCounts {
		totalRows += n
	}
	report := Report{
		TablesCopied:   len(rowCounts),
		TotalRows:      totalRows,
		Duration:       time.Since(start).String(),
		TableRowCounts: rowCounts,
		Backfilled:     backfilled,
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, fmt.Errorf("data_import_mismatch: marshal report: %w", err)
	}
	return raw, nil
}

func fileDigest(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

// openSQLiteReadOnly opens a SQLite database file in read-only mode.
// The source file's hash/mtime are never modified.
func openSQLiteReadOnly(ctx context.Context, dsn string) (*sql.DB, error) {
	if _, err := os.Stat(dsn); err != nil {
		return nil, fmt.Errorf("source file not accessible: %w", err)
	}
	// URI mode=ro enforces a read-only source and immutable=1 prevents SQLite
	// from creating journal/WAL side files or updating file metadata.
	fullDSN := "file:" + url.PathEscape(dsn) + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", fullDSN)
	if err != nil {
		return nil, fmt.Errorf("open read-only SQLite source: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping read-only SQLite source: %w", err)
	}
	return db, nil
}

// targetTableIntersect returns source tables that exist in the active PostgreSQL schema.
// Callers reject any missing source table instead of silently dropping data.
func targetTableIntersect(ctx context.Context, db *sql.DB, sourceTables []string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name <> 'schema_migrations'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	targetSet := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		targetSet[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var result []string
	for _, t := range sourceTables {
		if _, ok := targetSet[t]; ok {
			result = append(result, t)
		}
	}
	return result, nil
}

func missingTables(sourceTables, targetTables []string) []string {
	targetSet := make(map[string]struct{}, len(targetTables))
	for _, table := range targetTables {
		targetSet[table] = struct{}{}
	}
	missing := make([]string, 0)
	for _, table := range sourceTables {
		if _, ok := targetSet[table]; !ok {
			missing = append(missing, table)
		}
	}
	return missing
}
