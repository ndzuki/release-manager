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

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, error_code, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		pl.ID, opID, opTerminalAt,
		stages, pl.Overall, pl.ErrorCode,
		pl.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert preflight lifecycle: %w", err)
	}
	return nil
}

func (s *preflightLifecycleStore) SetOperationTerminal(ctx context.Context, operationID string, terminalAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
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
