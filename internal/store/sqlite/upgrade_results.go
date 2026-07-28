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

type operationEventStore struct{ db *sql.DB }

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

func (s *operationEventStore) List(ctx context.Context, operationID string) ([]*store.OperationEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, operation_id, event_type, payload, created_at
		FROM operation_events
		WHERE operation_id = ?
		ORDER BY created_at, id
	`, operationID)
	if err != nil {
		return nil, fmt.Errorf("list operation events: %w", err)
	}
	defer rows.Close()

	var events []*store.OperationEvent
	for rows.Next() {
		var event store.OperationEvent
		var createdAt string
		if err := rows.Scan(&event.ID, &event.OperationID, &event.EventType, &event.Payload, &createdAt); err != nil {
			return nil, fmt.Errorf("scan operation event: %w", err)
		}
		event.CreatedAt, _ = time.Parse(time.RFC3339, createdAt) //nolint:errcheck // stored timestamps are RFC3339
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operation events: %w", err)
	}
	return events, nil
}
