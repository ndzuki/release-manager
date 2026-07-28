package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// CreateWithEvent atomically records an audit export and its companion event.
func (s *auditExportStore) CreateWithEvent(
	ctx context.Context,
	export *store.AuditExport,
	event *store.AuditEvent,
) error {
	if export.CreatedAt.IsZero() {
		export.CreatedAt = time.Now().UTC()
	}
	if export.Status == "" {
		export.Status = "pending"
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = export.CreatedAt
	}
	if event.Metadata == nil {
		event.Metadata = map[string]string{}
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("marshal audit export event metadata: %w", err)
	}

	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin audit export: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after successful commit

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_exports (id, organization_id, since, until, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`,
		export.ID,
		export.OrganizationID,
		export.Since.UTC().Format(time.RFC3339),
		export.Until.UTC().Format(time.RFC3339),
		export.Status,
		export.CreatedAt.UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("insert audit export: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (id, actor_kind, actor_id, organization_id, role,
			resource_type, resource_id, action, status, duration_ms,
			change_summary, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.ID,
		string(event.ActorKind),
		event.ActorID,
		event.OrganizationID,
		event.Role,
		event.ResourceType,
		event.ResourceID,
		event.Action,
		event.Status,
		event.DurationMs,
		event.ChangeSummary,
		string(metadata),
		event.CreatedAt.UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("insert audit export event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit audit export: %w", err)
	}
	return nil
}
