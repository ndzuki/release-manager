package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
)

type preflightLifecycleStore struct{ db *sql.DB }

// CreateOrReset implements the REQ-019 two-phase start write: the first insert
// records overall=running with empty stages; a retry for the same operation
// resets the row to running while preserving any operation_terminal_at already
// backfilled by the operation terminal transaction (TASK-023).
func (s *preflightLifecycleStore) CreateOrReset(ctx context.Context, operationID string) (*store.PreflightLifecycle, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	id := uuid.New().String()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, created_at, updated_at)
		VALUES (?, ?, (SELECT terminal_at FROM operations WHERE id = ?), '', 'running', ?, ?)
		ON CONFLICT (operation_id) DO UPDATE SET
			stages = '',
			overall = 'running',
			updated_at = excluded.updated_at,
			operation_terminal_at = COALESCE(preflight_lifecycles.operation_terminal_at, excluded.operation_terminal_at)
	`, id, operationID, operationID, now, now)
	if err != nil {
		return nil, fmt.Errorf("create or reset preflight lifecycle: %w", err)
	}
	return s.GetByOperationID(ctx, operationID)
}

// UpdateResult persists the final overall and canonical stage list (REQ-019
// two-phase complete write).
func (s *preflightLifecycleStore) UpdateResult(ctx context.Context, operationID, overall, stages string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, `
		UPDATE preflight_lifecycles
		SET overall = ?, stages = ?, updated_at = ?
		WHERE operation_id = ?
	`, overall, stages, now, operationID)
	if err != nil {
		return fmt.Errorf("update preflight lifecycle result: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("preflight lifecycle result rows affected: %w", err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// GetByOperationID returns the lifecycle record for an operation.
func (s *preflightLifecycleStore) GetByOperationID(ctx context.Context, operationID string) (*store.PreflightLifecycle, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, operation_id, operation_terminal_at, stages, overall, created_at, updated_at
		FROM preflight_lifecycles
		WHERE operation_id = ?
	`, operationID)

	var (
		pl                  store.PreflightLifecycle
		storedOperationID   *string
		operationTerminalAt *string
		createdAt           string
		updatedAt           string
	)
	if err := row.Scan(&pl.ID, &storedOperationID, &operationTerminalAt, &pl.Stages, &pl.Overall, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan preflight lifecycle: %w", err)
	}
	pl.OperationID = storedOperationID

	parsedCreatedAt, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse preflight lifecycle created_at: %w", err)
	}
	pl.CreatedAt = parsedCreatedAt

	parsedUpdatedAt, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse preflight lifecycle updated_at: %w", err)
	}
	pl.UpdatedAt = parsedUpdatedAt

	if operationTerminalAt != nil {
		parsedTerminalAt, err := time.Parse(time.RFC3339, *operationTerminalAt)
		if err != nil {
			return nil, fmt.Errorf("parse preflight lifecycle terminal_at: %w", err)
		}
		pl.OperationTerminalAt = &parsedTerminalAt
	}
	return &pl, nil
}

// DeleteExpired removes lifecycle records past their retention in bounded
// batches (REQ-069 Phase 4, AC-069-23/24/25/26): operation-linked rows past
// ttl from operation_terminal_at, orphan rows past orphanTTL from created_at,
// and rows whose linked operation is terminal without a backfilled
// operation_terminal_at, evaluated from operations.terminal_at.
func (s *preflightLifecycleStore) DeleteExpired(ctx context.Context, ttl, orphanTTL time.Duration, limits ...int) (int64, error) {
	limit := 100
	if len(limits) > 0 && limits[0] > 0 && limits[0] < limit {
		limit = limits[0]
	}
	now := time.Now().UTC()
	ttlCutoff := now.Add(-ttl).Format(time.RFC3339)
	orphanCutoff := now.Add(-orphanTTL).Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, `DELETE FROM preflight_lifecycles WHERE rowid IN (
		SELECT pl.rowid FROM preflight_lifecycles AS pl
		LEFT JOIN operations AS o ON o.id = pl.operation_id
		WHERE (pl.operation_id IS NULL AND pl.created_at < ?)
		   OR (pl.operation_terminal_at IS NOT NULL AND pl.operation_terminal_at < ?)
		   OR (pl.operation_terminal_at IS NULL
		       AND o.id IS NOT NULL
		       AND o.status IN ('succeeded', 'failed', 'cancelled', 'timeout')
		       AND o.terminal_at IS NOT NULL
		       AND o.terminal_at < ?)
		ORDER BY pl.created_at, pl.id LIMIT ?
	)`, orphanCutoff, ttlCutoff, ttlCutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired preflight lifecycles: %w", err)
	}
	return result.RowsAffected()
}
