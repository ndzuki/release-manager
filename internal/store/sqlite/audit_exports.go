package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type auditExportStore struct{ db *sql.DB }

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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin audit export: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after successful commit

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_exports (
			id, organization_id, since, until, status, download_url, error_message, created_at, completed_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		export.ID,
		export.OrganizationID,
		export.Since.UTC().Format(time.RFC3339Nano),
		export.Until.UTC().Format(time.RFC3339Nano),
		export.Status,
		export.DownloadURL,
		export.ErrorMessage,
		export.CreatedAt.UTC().Format(time.RFC3339Nano),
		nullableTime(export.CompletedAt),
	); err != nil {
		return fmt.Errorf("insert audit export: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (id, actor_kind, actor_id, actor_name, organization_id, role,
			resource_type, resource_id, action, status, operation_id, request_id, duration_ms,
			change_summary, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.ID,
		string(event.ActorKind),
		event.ActorID,
		event.ActorName,
		event.OrganizationID,
		event.Role,
		event.ResourceType,
		event.ResourceID,
		event.Action,
		event.Status,
		event.OperationID,
		event.RequestID,
		event.DurationMs,
		event.ChangeSummary,
		string(metadata),
		event.CreatedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("insert audit export event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit audit export: %w", err)
	}
	return nil
}

func (s *auditExportStore) Get(ctx context.Context, id string) (*store.AuditExport, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, organization_id, since, until, status, download_url, error_message, created_at, completed_at
		FROM audit_exports
		WHERE id = ?
	`, id)
	var export store.AuditExport
	var since, until, createdAt string
	var completedAt sql.NullString
	if err := row.Scan(
		&export.ID,
		&export.OrganizationID,
		&since,
		&until,
		&export.Status,
		&export.DownloadURL,
		&export.ErrorMessage,
		&createdAt,
		&completedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan audit export: %w", err)
	}
	var err error
	export.Since, err = time.Parse(time.RFC3339Nano, since)
	if err != nil {
		return nil, fmt.Errorf("parse audit export since: %w", err)
	}
	export.Until, err = time.Parse(time.RFC3339Nano, until)
	if err != nil {
		return nil, fmt.Errorf("parse audit export until: %w", err)
	}
	export.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse audit export created_at: %w", err)
	}
	if completedAt.Valid {
		completed, parseErr := time.Parse(time.RFC3339Nano, completedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse audit export completed_at: %w", parseErr)
		}
		export.CompletedAt = &completed
	}
	return &export, nil
}

func (s *auditExportStore) UpdateStatus(
	ctx context.Context,
	id, status, downloadURL, errorMessage string,
	completedAt *time.Time,
) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE audit_exports
		SET status = ?, download_url = ?, error_message = ?, completed_at = ?
		WHERE id = ?
	`, status, downloadURL, errorMessage, nullableTime(completedAt), id)
	if err != nil {
		return fmt.Errorf("update audit export status: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("audit export rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
