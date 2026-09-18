package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// notificationOutboxStore delivers queued terminal notifications (REQ-031
// AC-031-12). The operation terminal transition writes the entries; this reads
// and acknowledges them.
type notificationOutboxStore struct{ db *DB }

// NotificationOutbox returns the terminal-notification outbox consumer.
func (s *Store) NotificationOutbox() store.NotificationOutboxStore {
	return &notificationOutboxStore{db: s.db}
}

// ListUndelivered returns the oldest undelivered entries. Ordering by
// (created_at, id) keeps the order stable when several entries share a
// timestamp, so a retry walks the queue in the same order.
func (s *notificationOutboxStore) ListUndelivered(ctx context.Context, limit int) ([]*store.ApprovalOutboxEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_type, payload_json, created_at, delivered, delivered_at
		FROM notification_outbox
		WHERE delivered = FALSE
		ORDER BY created_at, id
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list undelivered notification outbox: %w", err)
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
			return nil, fmt.Errorf("scan notification outbox: %w", err)
		}
		out = append(out, &store.ApprovalOutboxEntry{
			ID: id, EventType: eventType, PayloadJSON: payload,
			CreatedAt: createdAt, Delivered: delivered, DeliveredAt: deliveredAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notification outbox: %w", err)
	}
	return out, nil
}

// MarkDelivered acknowledges one entry. Acknowledging twice is not an error:
// at-least-once delivery means the worker may repeat an acknowledgement after a
// crash, and failing there would turn a duplicate into a stuck queue.
func (s *notificationOutboxStore) MarkDelivered(ctx context.Context, id string, at time.Time) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE notification_outbox SET delivered = TRUE, delivered_at = ? WHERE id = ?`, at.UTC(), id)
	if err != nil {
		return fmt.Errorf("mark notification outbox delivered: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("notification outbox rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}
