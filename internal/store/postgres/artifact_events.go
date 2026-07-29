package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
)

type artifactEventStore struct{ gorm *DB }

func (s *artifactEventStore) CreateTx(tx *gorm.DB, event *store.ArtifactEvent) error {
	if tx == nil {
		return fmt.Errorf("create artifact event: nil transaction")
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.ReceivedAt.IsZero() {
		event.ReceivedAt = time.Now().UTC()
	}
	result := tx.Exec(`
		INSERT INTO artifact_events
			(id, source_id, event_id, event_type, occurred_at, received_at,
			 raw_payload, payload_sha256, artifact_type, repository)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source_id, event_id) DO NOTHING
	`, event.ID, event.SourceID, event.EventID, event.EventType,
		event.OccurredAt.UTC(), event.ReceivedAt.UTC(), event.RawPayload,
		event.PayloadSHA256, string(event.ArtifactType), event.Repository)
	if result.Error != nil {
		return fmt.Errorf("create artifact event: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrDuplicateKey
	}
	return nil
}

func (s *artifactEventStore) GetBySourceAndEvent(ctx context.Context, sourceID, eventID string) (*store.ArtifactEvent, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT id, source_id, event_id, event_type, occurred_at, received_at,
			raw_payload, payload_sha256, artifact_type, repository
		FROM artifact_events
		WHERE source_id = ? AND event_id = ?
	`, sourceID, eventID)
	var event store.ArtifactEvent
	var artifactType string
	if err := row.Scan(&event.ID, &event.SourceID, &event.EventID, &event.EventType,
		&event.OccurredAt, &event.ReceivedAt, &event.RawPayload, &event.PayloadSHA256,
		&artifactType, &event.Repository); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get artifact event: %w", err)
	}
	event.ArtifactType = store.ArtifactType(artifactType)
	return &event, nil
}
