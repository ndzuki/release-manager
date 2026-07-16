package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type auditEventStore struct{ db *sql.DB }

func (s *auditEventStore) Create(ctx context.Context, e *store.AuditEvent) error {
	metaJSON, err := json.Marshal(e.Metadata)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audit_events (id, actor_kind, actor_id, organization_id, role,
			resource_type, resource_id, action, status, duration_ms,
			change_summary, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		e.ID, string(e.ActorKind), e.ActorID, e.OrganizationID, e.Role,
		e.ResourceType, e.ResourceID, e.Action, e.Status, e.DurationMs,
		e.ChangeSummary, string(metaJSON), e.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

func (s *auditEventStore) CreateBatch(ctx context.Context, events []*store.AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after successful Commit

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO audit_events (id, actor_kind, actor_id, organization_id, role,
			resource_type, resource_id, action, status, duration_ms,
			change_summary, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare batch insert: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for _, e := range events {
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		metaJSON, err := json.Marshal(e.Metadata)
		if err != nil {
			return fmt.Errorf("marshal audit metadata for %s: %w", e.ID, err)
		}
		if _, err := stmt.ExecContext(ctx,
			e.ID, string(e.ActorKind), e.ActorID, e.OrganizationID, e.Role,
			e.ResourceType, e.ResourceID, e.Action, e.Status, e.DurationMs,
			e.ChangeSummary, string(metaJSON), e.CreatedAt.UTC().Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("batch insert audit event %s: %w", e.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch: %w", err)
	}
	return nil
}
