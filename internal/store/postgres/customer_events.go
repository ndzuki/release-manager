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
