package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type outboxStore struct{ db *sql.DB }

func (s *outboxStore) Create(ctx context.Context, e *store.OutboxEntry) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = e.CreatedAt
	}
	if len(e.Payload) == 0 {
		e.Payload = []byte{}
	}
	if e.Status == "" {
		e.Status = store.CommandPending
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO outbox (id, operation_id, operator_id, payload, status, max_inflight, result_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		e.ID, e.OperationID, e.OperatorID, e.Payload, string(e.Status),
		e.MaxInFlight, e.ResultJSON,
		e.CreatedAt.UTC().Format(time.RFC3339), e.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert outbox entry: %w", err)
	}
	return nil
}

func (s *outboxStore) Get(ctx context.Context, id string) (*store.OutboxEntry, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, operation_id, operator_id, payload, status, max_inflight, result_json,
       created_at, updated_at, delivered_at, acked_at
FROM outbox WHERE id = ?
`, id)
	return scanOutboxEntry(row)
}

func (s *outboxStore) GetPendingForOperator(ctx context.Context, operatorID string) (*store.OutboxEntry, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, operation_id, operator_id, payload, status, max_inflight, result_json,
       created_at, updated_at, delivered_at, acked_at
FROM outbox WHERE operator_id=? AND status='pending' ORDER BY created_at ASC LIMIT 1
`, operatorID)
	return scanOutboxEntry(row)
}

func (s *outboxStore) UpdateStatus(ctx context.Context, id string, status store.CommandStatus, resultJSON string) error {
	now := time.Now().UTC().Format(time.RFC3339)

	var extraCol string
	switch status {
	case store.CommandDelivered:
		extraCol = ", delivered_at=?"
	case store.CommandPersisted:
		extraCol = ", acked_at=?"
	}


	//nolint:gosec // extraCol is a static column name, not user input
	query := fmt.Sprintf(`UPDATE outbox SET status=?, result_json=?, updated_at=?%s WHERE id=?`, extraCol)

	var args []interface{}
	args = append(args, string(status), resultJSON, now)
	if extraCol != "" {
		args = append(args, now)
	}
	args = append(args, id)

	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update outbox status: %w", err)
	}
	return nil
}

func (s *outboxStore) GetNextPending(ctx context.Context, operatorID string) (*store.OutboxEntry, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, operation_id, operator_id, payload, status, max_inflight, result_json,
       created_at, updated_at, delivered_at, acked_at
FROM outbox WHERE operator_id=? AND status='pending' ORDER BY created_at ASC LIMIT 1
`, operatorID)
	return scanOutboxEntry(row)
}

func scanOutboxEntry(row interface{ Scan(...interface{}) error }) (*store.OutboxEntry, error) {
	var (
		id, operationID, operatorID, status, resultJSON, createdAt, updatedAt string
		maxInFlight                                                           int
		payload                                                               []byte
		deliveredAt, ackedAt                                                 *string
	)
	if err := row.Scan(&id, &operationID, &operatorID, &payload, &status,
		&maxInFlight, &resultJSON, &createdAt, &updatedAt, &deliveredAt, &ackedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan outbox entry: %w", err)
	}

	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse outbox created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse outbox updated_at: %w", err)
	}

	e := &store.OutboxEntry{
		ID:          id,
		OperationID: operationID,
		OperatorID:  operatorID,
		Payload:     payload,
		Status:      store.CommandStatus(status),
		MaxInFlight: maxInFlight,
		ResultJSON:  resultJSON,
		CreatedAt:   ct,
		UpdatedAt:   ut,
	}

	if deliveredAt != nil {
		t, err := time.Parse(time.RFC3339, *deliveredAt)
		if err != nil {
			return nil, fmt.Errorf("parse outbox delivered_at: %w", err)
		}
		e.DeliveredAt = &t
	}
	if ackedAt != nil {
		t, err := time.Parse(time.RFC3339, *ackedAt)
		if err != nil {
			return nil, fmt.Errorf("parse outbox acked_at: %w", err)
		}
		e.AckedAt = &t
	}

	return e, nil
}
