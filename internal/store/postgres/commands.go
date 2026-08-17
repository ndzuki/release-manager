package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type outboxStore struct{ gorm *DB }

const outboxColumns = `id, command_id, operation_id, operation_type, operator_id, payload, status, max_inflight, sequence, result_json, created_at, updated_at, delivered_at, acked_at`

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
	if e.CommandID == "" {
		e.CommandID = e.ID
	}

	_, err := s.gorm.ExecContext(ctx, `
INSERT INTO outbox (id, command_id, operation_id, operation_type, operator_id, payload, status, max_inflight, sequence, result_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		e.ID, e.CommandID, e.OperationID, e.OperationType, e.OperatorID, e.Payload, string(e.Status),
		e.MaxInFlight, e.Sequence, e.ResultJSON,
		e.CreatedAt.UTC().Format(time.RFC3339), e.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert outbox entry: %w", err)
	}
	return nil
}

func (s *outboxStore) Get(ctx context.Context, id string) (*store.OutboxEntry, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE id = ?`, id)
	return scanOutboxEntry(row)
}

func (s *outboxStore) GetByCommandID(ctx context.Context, commandID string) (*store.OutboxEntry, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE command_id = ? ORDER BY created_at DESC LIMIT 1`, commandID)
	return scanOutboxEntry(row)
}

func (s *outboxStore) GetPendingForOperator(ctx context.Context, operatorID string) (*store.OutboxEntry, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE operator_id=? AND status='pending' ORDER BY sequence ASC LIMIT 1`, operatorID)
	return scanOutboxEntry(row)
}

func (s *outboxStore) GetDeliveredNotAcked(ctx context.Context, operatorID string) ([]*store.OutboxEntry, error) {
	rows, err := s.gorm.QueryContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE operator_id=? AND status IN ('delivered','persisted','running') AND acked_at IS NULL ORDER BY sequence ASC`, operatorID)
	if err != nil {
		return nil, fmt.Errorf("query delivered not acked: %w", err)
	}
	defer rows.Close()

	var entries []*store.OutboxEntry
	for rows.Next() {
		entry, err := scanOutboxEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *outboxStore) GetInflightForOperator(ctx context.Context, operatorID string) (*store.OutboxEntry, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE operator_id=? AND status IN ('delivered', 'persisted', 'running') ORDER BY sequence ASC LIMIT 1`, operatorID)
	return scanOutboxEntry(row)
}

func (s *outboxStore) GetByOperationID(ctx context.Context, operationID string) (*store.OutboxEntry, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE operation_id = ? ORDER BY created_at DESC LIMIT 1`, operationID)
	return scanOutboxEntry(row)
}

func (s *outboxStore) GetNextSequence(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	err := s.gorm.QueryRowContext(ctx, `SELECT MAX(sequence) FROM outbox`).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("get next sequence: %w", err)
	}
	if seq.Valid {
		return seq.Int64 + 1, nil
	}
	return 1, nil
}

func (s *outboxStore) UpdateSequence(ctx context.Context, id string, sequence int64) error {
	result, err := s.gorm.ExecContext(ctx, `UPDATE outbox SET sequence=?, updated_at=? WHERE id=?`,
		sequence, time.Now().UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("update outbox sequence: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("outbox sequence rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
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

	_, err := s.gorm.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update outbox status: %w", err)
	}
	return nil
}

func (s *outboxStore) PersistAck(ctx context.Context, id string) (*store.OperationTimelineEntry, error) {
	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin outbox ack: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var operationID, commandID string
	now := time.Now().UTC().Format(time.RFC3339)
	err = tx.QueryRowContext(ctx, `
		UPDATE outbox SET status = ?, acked_at = ?, updated_at = ?
		WHERE id = ? AND status != ?
		RETURNING operation_id, command_id
	`, string(store.CommandPersisted), now, now, id, string(store.CommandPersisted)).Scan(&operationID, &commandID)
	if err == sql.ErrNoRows {
		return nil, nil // already persisted: idempotent replay, no second entry
	}
	if err != nil {
		return nil, fmt.Errorf("persist outbox ack: %w", err)
	}
	data, err := json.Marshal(store.AckTimelineData{RequestID: commandID, AckStage: "persisted"})
	if err != nil {
		return nil, fmt.Errorf("encode outbox ack timeline: %w", err)
	}
	entry, err := appendTimelineEntry(ctx, tx, &store.OperationTimelineEntry{
		OperationID: operationID, Kind: string(store.TimelineEntryACK), Data: data,
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit outbox ack: %w", err)
	}
	return entry, nil
}

func (s *outboxStore) GetNextPending(ctx context.Context, operatorID string) (*store.OutboxEntry, error) {
	row := s.gorm.QueryRowContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE operator_id=? AND status='pending' ORDER BY sequence ASC LIMIT 1`, operatorID)
	return scanOutboxEntry(row)
}

func scanOutboxEntry(row interface{ Scan(...interface{}) error }) (*store.OutboxEntry, error) {
	var (
		id, commandID, operationID, operationType, operatorID, status, resultJSON, createdAt, updatedAt string
		maxInFlight                                                                                     int
		sequence                                                                                        int64
		payload                                                                                         []byte
		deliveredAt, ackedAt                                                                            *string
	)
	if err := row.Scan(&id, &commandID, &operationID, &operationType, &operatorID, &payload, &status,
		&maxInFlight, &sequence, &resultJSON, &createdAt, &updatedAt, &deliveredAt, &ackedAt); err != nil {
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
		ID:            id,
		CommandID:     commandID,
		OperationID:   operationID,
		OperationType: operationType,
		OperatorID:    operatorID,
		Payload:       payload,
		Status:        store.CommandStatus(status),
		MaxInFlight:   maxInFlight,
		Sequence:      sequence,
		ResultJSON:    resultJSON,
		CreatedAt:     ct,
		UpdatedAt:     ut,
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
