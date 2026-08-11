package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type customerEventStore struct{ gorm *DB }

func (s *customerEventStore) Create(ctx context.Context, ev *store.CustomerEvent) error {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}

	_, err := s.gorm.ExecContext(ctx, `
INSERT INTO customer_events (id, customer_id, event_type, created_at)
VALUES (?, ?, ?, ?)
`,
		ev.ID, ev.CustomerID, ev.EventType,
		ev.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert customer event: %w", err)
	}
	return nil
}

// ListByCustomer returns the immutable lifecycle history for one customer,
// ordered by (created_at DESC, id DESC) for a stable newest-first view.
func (s *customerEventStore) ListByCustomer(ctx context.Context, customerID string) ([]*store.CustomerEvent, error) {
	rows, err := s.gorm.QueryContext(ctx, `
SELECT id, customer_id, event_type, created_at
FROM customer_events
WHERE customer_id = ?
ORDER BY created_at DESC, id DESC`, customerID)
	if err != nil {
		return nil, fmt.Errorf("list customer events: %w", err)
	}
	defer rows.Close()

	var events []*store.CustomerEvent
	for rows.Next() {
		var (
			event     store.CustomerEvent
			createdAt string
		)
		if err := rows.Scan(&event.ID, &event.CustomerID, &event.EventType, &createdAt); err != nil {
			return nil, fmt.Errorf("scan customer event: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse customer event created_at: %w", err)
		}
		event.CreatedAt = parsed
		events = append(events, &event)
	}
	return events, rows.Err()
}
