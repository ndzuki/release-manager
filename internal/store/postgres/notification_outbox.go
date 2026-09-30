package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// Outbox tables this store reads. They share a shape on purpose: a row is written inside the
// transition that owns it and delivered by exactly one drain.
const (
	notificationOutboxTable = "notification_outbox"
	auditOutboxTable        = "audit_outbox"
)

// approvalOutboxStore delivers queued outbox entries: terminal notifications (REQ-031 AC-031-12) and,
// since TASK-231, the audit rows that approval and bundle transitions write in their own
// transaction.
//
// Both queues share ONE implementation on purpose. They are read the same way, acknowledged the
// same way and must not diverge: the audit drain was missing entirely for a while precisely
// because the audit side had no counterpart to this reader.
type approvalOutboxStore struct {
	db    *DB
	table string
}

// NotificationOutbox returns the terminal-notification outbox consumer.
func (s *Store) NotificationOutbox() store.NotificationOutboxStore {
	return &approvalOutboxStore{db: s.db, table: notificationOutboxTable}
}

// AuditOutbox returns the audit-outbox consumer.
func (s *Store) AuditOutbox() store.AuditOutboxStore {
	return &approvalOutboxStore{db: s.db, table: auditOutboxTable}
}

// ListUndelivered returns the oldest undelivered entries. Ordering by
// (created_at, id) keeps the order stable when several entries share a
// timestamp, so a retry walks the queue in the same order.
func (s *approvalOutboxStore) ListUndelivered(ctx context.Context, limit int) ([]*store.ApprovalOutboxEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	//nolint:gosec // the table name is one of the internal constants above, never caller input.
	query := `
		SELECT id, event_type, payload_json, created_at, delivered, delivered_at
		FROM ` + s.table + `
		WHERE delivered = FALSE
		ORDER BY created_at, id
		LIMIT ?
	`
	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("list undelivered %s: %w", s.table, err)
	}
	defer rows.Close() //nolint:errcheck // Read-only query.

	var out []*store.ApprovalOutboxEntry
	for rows.Next() {
		var (
			id, eventType string
			payload       []byte
			createdAt     time.Time
			delivered     bool
			deliveredAt   *time.Time
		)
		if err := rows.Scan(&id, &eventType, &payload, &createdAt, &delivered, &deliveredAt); err != nil {
			return nil, fmt.Errorf("scan outbox entry: %w", err)
		}
		out = append(out, &store.ApprovalOutboxEntry{
			ID: id, EventType: eventType, PayloadJSON: payload,
			CreatedAt: createdAt, Delivered: delivered, DeliveredAt: deliveredAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", s.table, err)
	}
	return out, nil
}

// MarkDelivered acknowledges one entry. Acknowledging twice is not an error:
// at-least-once delivery means the worker may repeat an acknowledgement after a
// crash, and failing there would turn a duplicate into a stuck queue.
func (s *approvalOutboxStore) MarkDelivered(ctx context.Context, id string, at time.Time) error {
	//nolint:gosec // the table name is one of the internal constants above, never caller input.
	query := `UPDATE ` + s.table + ` SET delivered = TRUE, delivered_at = ? WHERE id = ?`
	result, err := s.db.ExecContext(ctx, query, at.UTC(), id)
	if err != nil {
		return fmt.Errorf("mark %s delivered: %w", s.table, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("outbox rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}
