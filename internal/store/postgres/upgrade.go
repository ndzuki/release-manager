package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/store"
)

type executionResultReader struct{ s *Store }
type rolloutTrackingReader struct{ s *Store }

func (r executionResultReader) Get(ctx context.Context, operationID string) (*store.OperationExecutionResult, error) {
	return r.s.ExecutionResult(ctx, operationID)
}

func (r rolloutTrackingReader) Get(ctx context.Context, operationID string) (*store.RolloutTracking, error) {
	return r.s.RolloutTracking(ctx, operationID)
}

// FinalizeUpgrade atomically persists the typed result and all terminal projections.
//
//nolint:gocyclo // One transaction intentionally applies every terminal projection atomically.
func (s *Store) FinalizeUpgrade(ctx context.Context, input *store.UpgradeTerminalInput) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upgrade terminal transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM operation_execution_results WHERE operation_id = $1)`,
		input.OperationID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check upgrade result idempotency: %w", err)
	}
	if exists {
		return nil
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO operation_execution_results (operation_id, result_type, result_payload)
		VALUES ($1, 'upgrade', $2)
	`, input.OperationID, input.ResultPayload); err != nil {
		return fmt.Errorf("insert upgrade result: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE operations
		SET status = $1, state_version = state_version + 1, terminal_at = NOW(), last_error = $2, updated_at = NOW()
		WHERE id = $3 AND state_version = $4
	`, string(input.Status), input.LastError, input.OperationID, input.ExpectedStateVersion)
	if err != nil {
		return fmt.Errorf("terminal operation CAS: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("terminal operation rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrOptimisticLock
	}

	if input.UpdateInventory {
		result, err = tx.ExecContext(ctx, `
			UPDATE release_inventory
			SET revision = $1, values_digest = $2, observed_bundle_digest = $3, observed_chart_digest = $4,
			    observed_effective_values_digest = $5, observed_manifest_digest = $6, last_operation_id = $7,
			    inventory_status = $8, updated_at = NOW()
			WHERE release_definition_id = $9
		`, input.Revision, input.ObservedEffectiveValuesDigest, input.ObservedBundleDigest,
			input.ObservedChartDigest, input.ObservedEffectiveValuesDigest, input.ObservedManifestDigest,
			input.OperationID, string(input.InventoryStatus), input.ReleaseDefinitionID)
		if err != nil {
			return fmt.Errorf("update upgrade inventory: %w", err)
		}
		rows, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("upgrade inventory rows affected: %w", err)
		}
		if rows == 0 {
			return fmt.Errorf("update upgrade inventory: %w", store.ErrNotFound)
		}
	}

	if input.Status == store.StatusSucceeded {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO rollout_trackings (operation_id, status, resource_count)
			VALUES ($1, 'pending', $2)
			ON CONFLICT (operation_id) DO NOTHING
		`, input.OperationID, input.ResourceCount); err != nil {
			return fmt.Errorf("insert rollout tracking: %w", err)
		}
	}

	if err := insertOperationEvent(ctx, tx, &store.OperationStateChangedEvent{
		ID:            uuid.NewString(),
		OperationID:   input.OperationID,
		OperationType: store.OperationUpgrade,
		DefinitionID:  input.ReleaseDefinitionID,
		NewStatus:     input.Status,
		StateVersion:  input.ExpectedStateVersion + 1,
	}); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upgrade terminal transaction: %w", err)
	}
	return nil
}

// ExecutionResult returns the persisted typed result.
func (s *Store) ExecutionResult(ctx context.Context, operationID string) (*store.OperationExecutionResult, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT operation_id, result_type, result_payload, created_at
		FROM operation_execution_results WHERE operation_id = $1
	`, operationID)
	var result store.OperationExecutionResult
	if err := row.Scan(&result.OperationID, &result.ResultType, &result.ResultPayload, &result.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get operation execution result: %w", err)
	}
	result.CreatedAt = result.CreatedAt.UTC()
	return &result, nil
}

// RolloutTracking returns the rollout record created for a successful upgrade.
func (s *Store) RolloutTracking(ctx context.Context, operationID string) (*store.RolloutTracking, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT operation_id, status, resource_count, ready_count, failed_count,
		       COALESCE(last_error, ''), created_at, updated_at
		FROM rollout_trackings WHERE operation_id = $1
	`, operationID)
	var tracking store.RolloutTracking
	if err := row.Scan(
		&tracking.OperationID, &tracking.Status, &tracking.ResourceCount, &tracking.ReadyCount,
		&tracking.FailedCount, &tracking.LastError, &tracking.CreatedAt, &tracking.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get rollout tracking: %w", err)
	}
	tracking.CreatedAt = tracking.CreatedAt.UTC()
	tracking.UpdatedAt = tracking.UpdatedAt.UTC()
	return &tracking, nil
}
