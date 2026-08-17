package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
)

// CreateOrReset implements the REQ-019 two-phase start write: the first insert
// records overall=running with empty stages; a retry for the same operation
// resets the row to running while preserving any operation_terminal_at already
// backfilled by the operation terminal transaction (TASK-023).
func (s *preflightLifecycleStore) CreateOrReset(ctx context.Context, operationID string) (*store.PreflightLifecycle, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	id := uuid.New().String()
	_, err := s.gorm.ExecContext(ctx, `
		INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, created_at, updated_at)
		VALUES (?, ?, (SELECT terminal_at FROM operations WHERE id = ?), '', 'running', ?, ?)
		ON CONFLICT (operation_id) DO UPDATE SET
			stages = '',
			overall = 'running',
			updated_at = EXCLUDED.updated_at,
			operation_terminal_at = COALESCE(preflight_lifecycles.operation_terminal_at, EXCLUDED.operation_terminal_at)
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
	result, err := s.gorm.ExecContext(ctx, `
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
	row := s.gorm.QueryRowContext(ctx, `
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

	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	pl.CreatedAt = ct

	ut, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	pl.UpdatedAt = ut

	if operationTerminalAt != nil && *operationTerminalAt != "" {
		t, err := time.Parse(time.RFC3339, *operationTerminalAt)
		if err != nil {
			return nil, fmt.Errorf("parse operation_terminal_at: %w", err)
		}
		pl.OperationTerminalAt = &t
	}
	return &pl, nil
}

// DeleteExpired removes lifecycle records past their TTL (REQ-069).
func (s *preflightLifecycleStore) DeleteExpired(ctx context.Context, ttl time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-ttl).Format(time.RFC3339)
	result, err := s.gorm.ExecContext(ctx, `
		DELETE FROM preflight_lifecycles
		WHERE (operation_id IS NULL AND created_at < ?)
		   OR (operation_id IS NOT NULL AND operation_terminal_at IS NOT NULL AND operation_terminal_at < ?)
	`, cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete expired preflight lifecycles: %w", err)
	}
	return result.RowsAffected()
}
