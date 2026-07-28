// Package postgres provides PostgreSQL persistence for release-manager.
package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ndzuki/release-manager/internal/store"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Store shares one pgx-backed database/sql pool for transactional and raw SQL access.
type Store struct {
	db *sql.DB
}

// Open opens and verifies a PostgreSQL connection pool.
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	return New(db), nil
}

// New constructs a Store around the shared physical pool required by ADR-014.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}


// UpgradeResults returns the atomic upgrade terminal writer.
func (s *Store) UpgradeResults() store.UpgradeResultStore { return s }

// ExecutionResults returns the typed result reader.
func (s *Store) ExecutionResults() store.OperationExecutionResultStore { return executionResultReader{s: s} }

// RolloutTrackings returns the rollout tracking reader.
func (s *Store) RolloutTrackings() store.RolloutTrackingStore { return rolloutTrackingReader{s: s} }
// DB exposes the shared pool for transaction-bound raw SQL and GORM integration.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the shared pool.
func (s *Store) Close() error { return s.db.Close() }
