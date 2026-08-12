package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
)

// Create inserts a new preflight lifecycle record.
func (s *preflightLifecycleStore) Create(ctx context.Context, pl *store.PreflightLifecycle) error {
	if pl.ID == "" {
		pl.ID = uuid.New().String()
	}
	if pl.CreatedAt.IsZero() {
		pl.CreatedAt = time.Now().UTC()
	}

	var opID, opTerminalAt *string
	if pl.OperationID != nil {
		opID = pl.OperationID
	}
	if pl.OperationTerminalAt != nil {
		t := pl.OperationTerminalAt.UTC().Format(time.RFC3339)
		opTerminalAt = &t
	}

	stages := string(pl.Stages)
	if stages == "" {
		stages = "[]"
	}

	now := pl.CreatedAt.UTC().Format(time.RFC3339)

	_, err := s.gorm.ExecContext(ctx, `
		INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, error_code, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		pl.ID, opID, opTerminalAt,
		stages, pl.Overall, pl.ErrorCode,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("insert preflight lifecycle: %w", err)
	}
	return nil
}

// GetByOperationID returns the most recent lifecycle record for an operation.
func (s *preflightLifecycleStore) GetByOperationID(ctx context.Context, operationID string) (*store.PreflightLifecycle, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT id, operation_id, operation_terminal_at, stages, overall, error_code, created_at
		FROM preflight_lifecycles
		WHERE operation_id = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, operationID)

	var (
		pl                  store.PreflightLifecycle
		storedOperationID   *string
		operationTerminalAt *string
		stages              string
		createdAt           string
	)
	if err := row.Scan(&pl.ID, &storedOperationID, &operationTerminalAt, &stages, &pl.Overall, &pl.ErrorCode, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan preflight lifecycle: %w", err)
	}
	pl.OperationID = storedOperationID
	pl.Stages = []byte(stages)
	if operationTerminalAt != nil && *operationTerminalAt != "" {
		t, err := time.Parse(time.RFC3339, *operationTerminalAt)
		if err != nil {
			return nil, fmt.Errorf("parse operation_terminal_at: %w", err)
		}
		pl.OperationTerminalAt = &t
	}
	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	pl.CreatedAt = ct
	return &pl, nil
}

// SetOperationTerminal records the terminal time for an on-going preflight operation.
func (s *preflightLifecycleStore) SetOperationTerminal(ctx context.Context, operationID string, terminalAt time.Time) error {
	result, err := s.gorm.ExecContext(ctx, `
		UPDATE preflight_lifecycles
		SET operation_terminal_at = ?
		WHERE operation_id = ? AND operation_terminal_at IS NULL
	`, terminalAt.UTC().Format(time.RFC3339), operationID)
	if err != nil {
		return fmt.Errorf("set preflight operation terminal: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("terminal rows affected: %w", err)
	}
	if n == 0 {
		return nil // no rows to update — not an error
	}
	return nil
}

// DeleteExpired removes lifecycle records past their TTL.
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
