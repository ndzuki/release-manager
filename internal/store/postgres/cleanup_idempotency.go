package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

const cleanupIdempotencyBatchLimit = 100

type cleanupIdempotencyStore struct{ db cleanupIdempotencyExecer }

type cleanupIdempotencyExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// TryCreate reserves a cleanup request key. A key created within the retention
// window is a conflict; an older leftover key is reusable.
func (s *cleanupIdempotencyStore) TryCreate(ctx context.Context, key string, retention time.Duration) error {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO cleanup_idempotency (idempotency_key, created_at)
		VALUES ($1, $2)
		ON CONFLICT (idempotency_key) DO UPDATE
		SET created_at = EXCLUDED.created_at
		WHERE cleanup_idempotency.created_at <= $3
	`, key, now, now.Add(-retention).UTC())
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrCleanupAlreadyRequested
		}
		return fmt.Errorf("insert cleanup idempotency key: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("cleanup idempotency rows affected: %w", err)
	}
	if rows != 1 {
		return store.ErrCleanupAlreadyRequested
	}
	return nil
}

func (s *cleanupIdempotencyStore) DeleteExpiredBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 || limit > cleanupIdempotencyBatchLimit {
		limit = cleanupIdempotencyBatchLimit
	}
	result, err := s.db.ExecContext(ctx, `
		WITH expired AS (
			SELECT ctid
			FROM cleanup_idempotency
			WHERE created_at < $1
			ORDER BY created_at, idempotency_key
			LIMIT $2
		)
		DELETE FROM cleanup_idempotency
		WHERE ctid IN (SELECT ctid FROM expired)
	`, cutoff.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired cleanup idempotency keys: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("deleted cleanup idempotency rows affected: %w", err)
	}
	if rows > int64(limit) {
		return 0, fmt.Errorf("delete expired cleanup idempotency keys: affected %d rows with limit %d", rows, limit)
	}
	return rows, nil
}
