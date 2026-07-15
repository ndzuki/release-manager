// Package sqlite provides a SQLite-backed implementation of the store interfaces.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
	"github.com/ndzuki/release-manager/internal/store"
)

// Store implements store.Store backed by SQLite.
type Store struct {
	db *sql.DB
	ops *operationStore
	defs *definitionStore
	vals *valuesStore
}

// Open creates a new SQLite-backed Store, running migrations on the database.
// The DSN must be a valid modernc.org/sqlite connection string.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}

	// WAL mode for better concurrent read performance.
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite pragma journal_mode: %w", err)
	}
	// Enable foreign keys.
	if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite pragma foreign_keys: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite migrate: %w", err)
	}

	s := &Store{db: db}
	s.ops = &operationStore{db: db}
	s.defs = &definitionStore{db: db}
	s.vals = &valuesStore{db: db}
	return s, nil
}

// Operations returns the OperationStore.
func (s *Store) Operations() store.OperationStore { return s.ops }

// Definitions returns the DefinitionStore.
func (s *Store) Definitions() store.DefinitionStore { return s.defs }

// Values returns the ValuesStore.
func (s *Store) Values() store.ValuesStore { return s.vals }

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying *sql.DB for testing.
func (s *Store) DB() *sql.DB { return s.db }

// migrate runs the ordered migration steps against the database.
func migrate(db *sql.DB) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback on a committed transaction is a no-op; error is irrelevant here.

	for _, stmt := range migrationStatements {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			return fmt.Errorf("migration statement: %w\nstmt: %s", err, stmt)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

// migrationStatements contains the ordered DDL for the core pipeline schema.
// Using IF NOT EXISTS so re-runs are idempotent.
var migrationStatements = []string{
	`CREATE TABLE IF NOT EXISTS release_definitions (
		id                  TEXT PRIMARY KEY,
		name                TEXT NOT NULL,
		customer_id         TEXT NOT NULL,
		cluster_id          TEXT NOT NULL,
		namespace           TEXT NOT NULL DEFAULT '',
		release_name        TEXT NOT NULL,
		chart_name          TEXT NOT NULL DEFAULT '',
		status              TEXT NOT NULL DEFAULT 'draft',
		optimistic_version  INTEGER NOT NULL DEFAULT 0,
		created_by          TEXT NOT NULL DEFAULT '',
		created_at          TEXT NOT NULL,
		updated_at          TEXT NOT NULL,
		UNIQUE(customer_id, cluster_id, namespace, release_name)
	)`,

	`CREATE TABLE IF NOT EXISTS values_revisions (
		id                    TEXT PRIMARY KEY,
		release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE CASCADE,
		revision              INTEGER NOT NULL DEFAULT 1,
		status                TEXT NOT NULL DEFAULT 'draft',
		"values"              BLOB NOT NULL,
		created_at            TEXT NOT NULL,
		updated_at            TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS operations (
		id                   TEXT PRIMARY KEY,
		operation_type       TEXT NOT NULL,
		status               TEXT NOT NULL DEFAULT 'pending',
		release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE CASCADE,
		idempotency_key      TEXT NOT NULL UNIQUE,
		request_hash         TEXT NOT NULL,
		state_version        INTEGER NOT NULL DEFAULT 0,
		bundle_id            TEXT NOT NULL DEFAULT '',
		values_revision_id   TEXT NOT NULL DEFAULT '',
		expected_revision    INTEGER NOT NULL DEFAULT 0,
		values_patch         BLOB,
		actor                TEXT NOT NULL DEFAULT '{}',
		created_at           TEXT NOT NULL,
		updated_at           TEXT NOT NULL,
		deadline             TEXT,
		last_error           TEXT NOT NULL DEFAULT ''
	)`,

	`CREATE INDEX IF NOT EXISTS idx_operations_definition ON operations(release_definition_id, status)`,
	`CREATE INDEX IF NOT EXISTS idx_operations_idempotency ON operations(idempotency_key)`,
	`CREATE INDEX IF NOT EXISTS idx_values_def ON values_revisions(release_definition_id)`,
}

// nowUTC returns the current time in UTC as an RFC3339 string for SQLite storage.
func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
