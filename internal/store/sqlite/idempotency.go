package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type idempotencyStore struct{ db *sql.DB }

func (s *idempotencyStore) CreateOrGet(ctx context.Context, record *store.IdempotencyRecord) (*store.IdempotencyRecord, bool, error) {
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = record.CreatedAt.Add(24 * time.Hour)
	}
	if record.ResponseRef == nil {
		record.ResponseRef = []byte{}
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO idempotency_records (id, scope, key, request_hash, response_ref, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, key) DO NOTHING
	`,
		record.ID, record.Scope, record.Key, record.RequestHash,
		record.ResponseRef, record.CreatedAt.UTC().Format(time.RFC3339),
		record.ExpiresAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, false, fmt.Errorf("insert idempotency record: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("rows affected: %w", err)
	}

	if rowsAffected > 0 {
		return record, true, nil
	}

	// Conflict — fetch the existing record to compare hashes.
	existing, err := s.getByScopeAndKey(ctx, record.Scope, record.Key)
	if err != nil {
		return nil, false, err
	}
	if existing.RequestHash != record.RequestHash {
		return nil, false, store.ErrIdempotencyConflict
	}

	return existing, false, nil
}

func (s *idempotencyStore) getByScopeAndKey(ctx context.Context, scope, key string) (*store.IdempotencyRecord, error) {
	var r store.IdempotencyRecord
	var createdAt, expiresAt string

	err := s.db.QueryRowContext(ctx, `
		SELECT id, scope, key, request_hash, response_ref, created_at, expires_at
		FROM idempotency_records
		WHERE scope = ? AND key = ?
	`, scope, key).Scan(&r.ID, &r.Scope, &r.Key, &r.RequestHash, &r.ResponseRef, &createdAt, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get idempotency record: %w", err)
	}

	r.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse idempotency record created_at: %w", err)
	}
	r.ExpiresAt, err = time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse idempotency record expires_at: %w", err)
	}
	return &r, nil
}

func (s *idempotencyStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM idempotency_records WHERE expires_at < ?
	`, before.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("delete expired idempotency records: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}
