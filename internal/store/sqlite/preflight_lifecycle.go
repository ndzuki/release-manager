package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type preflightLifecycleStore struct{ db *sql.DB }

func (s *preflightLifecycleStore) CreateOrReset(ctx context.Context, operationID string) error {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO preflight_lifecycles (
			id, operation_id, operation_terminal_at, stages, overall, created_at, updated_at
		)
		VALUES (?, ?, (SELECT terminal_at FROM operations WHERE id = ?), '', 'running', ?, ?)
		ON CONFLICT(operation_id) DO UPDATE SET
			stages = '',
			overall = 'running',
			updated_at = excluded.updated_at,
			operation_terminal_at = COALESCE(
				preflight_lifecycles.operation_terminal_at,
				excluded.operation_terminal_at
			)
	`, uuid.NewString(), operationID, operationID, now, now)
	if err != nil {
		return fmt.Errorf("create or reset preflight lifecycle: %w", err)
	}
	return nil
}

func (s *preflightLifecycleStore) UpdateResult(
	ctx context.Context,
	operationID, overall, stages string,
) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE preflight_lifecycles
		SET overall = ?, stages = ?, updated_at = ?
		WHERE operation_id = ?
	`, overall, stages, nowUTC(), operationID)
	if err != nil {
		return fmt.Errorf("update preflight lifecycle result: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("preflight lifecycle result rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("update preflight lifecycle result: operation %s not found", operationID)
	}
	return nil
}

func (s *preflightLifecycleStore) DeleteExpired(ctx context.Context, ttl time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-ttl).Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM preflight_lifecycles
		WHERE (operation_id IS NULL AND created_at < ?)
		   OR (operation_id IS NOT NULL AND operation_terminal_at IS NOT NULL AND operation_terminal_at < ?)
	`, cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete expired preflight lifecycles: %w", err)
	}
	return result.RowsAffected()
}
