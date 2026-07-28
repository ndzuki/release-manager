package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type operationExecutionResultStore struct{ db *sql.DB }

type rolloutTrackingStore struct{ db *sql.DB }

func (s *operationExecutionResultStore) Get(ctx context.Context, operationID string) (*store.OperationExecutionResult, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT operation_id, result_type, result_payload, created_at
		FROM operation_execution_results
		WHERE operation_id = ?
	`, operationID)

	var result store.OperationExecutionResult
	var createdAt string
	if err := row.Scan(&result.OperationID, &result.ResultType, &result.ResultPayload, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get operation execution result: %w", err)
	}
	result.CreatedAt, _ = time.Parse(time.RFC3339, createdAt) //nolint:errcheck // stored timestamps are RFC3339
	return &result, nil
}

func (s *rolloutTrackingStore) Get(ctx context.Context, operationID string) (*store.RolloutTracking, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT operation_id, status, resource_count, ready_count, failed_count, last_error, created_at, updated_at
		FROM rollout_trackings
		WHERE operation_id = ?
	`, operationID)

	var tracking store.RolloutTracking
	var createdAt, updatedAt string
	if err := row.Scan(
		&tracking.OperationID,
		&tracking.Status,
		&tracking.ResourceCount,
		&tracking.ReadyCount,
		&tracking.FailedCount,
		&tracking.LastError,
		&createdAt,
		&updatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get rollout tracking: %w", err)
	}
	tracking.CreatedAt, _ = time.Parse(time.RFC3339, createdAt) //nolint:errcheck // stored timestamps are RFC3339
	tracking.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt) //nolint:errcheck // stored timestamps are RFC3339
	return &tracking, nil
}
