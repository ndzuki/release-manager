package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type definitionEventStore struct{ db *sql.DB }

func (s *definitionEventStore) List(
	ctx context.Context,
	definitionID string,
) ([]*store.ReleaseDefinitionEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, definition_id, event_type, created_at
		FROM release_definition_events
		WHERE definition_id = ?
		ORDER BY created_at, id
	`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("list definition events: %w", err)
	}
	defer rows.Close()

	events := make([]*store.ReleaseDefinitionEvent, 0)
	for rows.Next() {
		event, err := scanDefinitionEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate definition events: %w", err)
	}
	return events, nil
}

func scanDefinitionEvent(row interface{ Scan(...interface{}) error }) (*store.ReleaseDefinitionEvent, error) {
	var (
		event     store.ReleaseDefinitionEvent
		createdAt string
	)
	if err := row.Scan(&event.ID, &event.DefinitionID, &event.EventType, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan definition event: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse definition event created_at: %w", err)
	}
	event.CreatedAt = parsed
	return &event, nil
}
