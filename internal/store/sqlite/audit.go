package sqlite

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
		e.ChangeSummary, string(metaJSON), e.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

func (s *auditEventStore) ListByResource(
	ctx context.Context,
	resourceType, resourceID string,
) ([]*store.AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, actor_kind, actor_id, organization_id, role,
			resource_type, resource_id, action, status, duration_ms,
			change_summary, metadata, created_at
		FROM audit_events
		WHERE resource_type = ? AND resource_id = ?
		ORDER BY created_at ASC
	`, resourceType, resourceID)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()

	var events []*store.AuditEvent
	for rows.Next() {
		var event store.AuditEvent
		var actorKind, metadataJSON, createdAt string
		if err := rows.Scan(
			&event.ID, &actorKind, &event.ActorID, &event.OrganizationID, &event.Role,
			&event.ResourceType, &event.ResourceID, &event.Action, &event.Status,
			&event.DurationMs, &event.ChangeSummary, &metadataJSON, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		event.ActorKind = store.AuditActorKind(actorKind)
		if err := json.Unmarshal([]byte(metadataJSON), &event.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshal audit metadata: %w", err)
		}
		event.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse audit created_at: %w", err)
		}
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return events, nil
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
			e.ChangeSummary, string(metaJSON), e.CreatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("batch insert audit event %s: %w", e.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch: %w", err)
	}
	return nil
}

const auditEventColumns = `id, actor_kind, actor_id, organization_id, role,
	resource_type, resource_id, action, status, duration_ms,
	change_summary, metadata, created_at`

// Query returns audit events in descending (created_at, id) order.
func (s *auditEventStore) Query(
	ctx context.Context,
	filter store.AuditEventFilter,
	cursor string,
	limit int,
) (*store.AuditEventPage, error) {
	if limit <= 0 {
		limit = 50
	}

	where, args := buildAuditWhere(filter)
	if cursor != "" {
		createdAt, id, err := decodeAuditCursor(cursor)
		if err != nil {
			return nil, err
		}
		where = append(where, `(created_at < ? OR (created_at = ? AND id < ?))`)
		createdAtString := createdAt.UTC().Format(time.RFC3339Nano)
		args = append(args, createdAtString, createdAtString, id)
	}

	query := `SELECT ` + auditEventColumns + ` FROM audit_events`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `) //nolint:gosec // fixed allowlisted clauses only
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit events: %w", err)
	}
	defer rows.Close()

	events := make([]*store.AuditEvent, 0, limit+1)
	for rows.Next() {
		event, err := scanAuditEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}

	page := &store.AuditEventPage{Events: events}
	if len(events) > limit {
		page.HasMore = true
		page.Events = events[:limit]
		last := page.Events[len(page.Events)-1]
		page.NextCursor = encodeAuditCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// GetByID returns a single audit event.
func (s *auditEventStore) GetByID(ctx context.Context, id string) (*store.AuditEvent, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+auditEventColumns+` FROM audit_events WHERE id = ?`, id)
	return scanAuditEvent(row)
}

// Count returns the number of audit events matching filter.
func (s *auditEventStore) Count(ctx context.Context, filter store.AuditEventFilter) (int64, error) {
	where, args := buildAuditWhere(filter)
	query := `SELECT COUNT(*) FROM audit_events`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `) //nolint:gosec // column names and clauses are fixed constants
	}

	var count int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count audit events: %w", err)
	}
	return count, nil
}

func buildAuditWhere(filter store.AuditEventFilter) (where []string, args []any) {
	where = make([]string, 0, 8)
	args = make([]any, 0, 8)
	addString := func(column, value string) {
		if value == "" {
			return
		}
		where = append(where, column+` = ?`)
		args = append(args, value)
	}

	addString("organization_id", filter.OrganizationID)
	addString("resource_type", filter.ResourceType)
	addString("resource_id", filter.ResourceID)
	addString("actor_id", filter.ActorID)
	addString("action", filter.Action)
	addString("status", filter.Status)
	if filter.Since != nil {
		where = append(where, `created_at >= ?`)
		args = append(args, filter.Since.UTC().Format(time.RFC3339Nano))
	}
	if filter.Until != nil {
		where = append(where, `created_at < ?`)
		args = append(args, filter.Until.UTC().Format(time.RFC3339Nano))
	}
	return where, args
}

func scanAuditEvent(row interface{ Scan(...any) error }) (*store.AuditEvent, error) {
	var (
		event        store.AuditEvent
		actorKind    string
		metadataJSON string
		createdAt    string
	)
	if err := row.Scan(
		&event.ID, &actorKind, &event.ActorID, &event.OrganizationID, &event.Role,
		&event.ResourceType, &event.ResourceID, &event.Action, &event.Status, &event.DurationMs,
		&event.ChangeSummary, &metadataJSON, &createdAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan audit event: %w", err)
	}

	event.ActorKind = store.AuditActorKind(actorKind)
	if err := json.Unmarshal([]byte(metadataJSON), &event.Metadata); err != nil {
		return nil, fmt.Errorf("unmarshal audit metadata: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse audit created_at: %w", err)
	}
	event.CreatedAt = parsed
	return &event, nil
}

func encodeAuditCursor(createdAt time.Time, id string) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeAuditCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", store.ErrInvalidCursor
	}
	createdAt, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return time.Time{}, "", store.ErrInvalidCursor
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return time.Time{}, "", store.ErrInvalidCursor
	}
	return parsed, id, nil
}
