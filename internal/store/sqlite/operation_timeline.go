package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/store"
)

type timelineStore struct{ db *sql.DB }

func (s *timelineStore) Append(ctx context.Context, entry *store.OperationTimelineEntry) (*store.OperationTimelineEntry, error) {
	if entry == nil || entry.OperationID == "" || entry.Kind == "" || entry.Sequence != 0 {
		return nil, fmt.Errorf("append operation timeline: invalid entry")
	}
	var appended *store.OperationTimelineEntry
	err := retryBusy(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin operation timeline append: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck // no-op after commit
		appended, err = appendTimelineEntry(ctx, tx, entry)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit operation timeline append: %w", err)
		}
		return nil
	})
	return appended, err
}

func appendTimelineEntry(ctx context.Context, tx *sql.Tx, entry *store.OperationTimelineEntry) (*store.OperationTimelineEntry, error) {
	appended := *entry
	if appended.ID == "" {
		appended.ID = uuid.NewString()
	}
	if appended.CreatedAt.IsZero() {
		appended.CreatedAt = time.Now().UTC()
	} else {
		appended.CreatedAt = appended.CreatedAt.UTC()
	}
	if len(appended.Data) == 0 {
		appended.Data = json.RawMessage(`{}`)
	}
	if !json.Valid(appended.Data) {
		return nil, fmt.Errorf("append operation timeline: invalid data")
	}
	if appended.Sequence != 0 {
		return nil, fmt.Errorf("append operation timeline: sequence is allocated by the store")
	}
	var parentID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM operations WHERE id = ?`, appended.OperationID).Scan(&parentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("lock operation timeline parent: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1
		FROM operation_timeline
		WHERE operation_id = ?
	`, appended.OperationID).Scan(&appended.Sequence); err != nil {
		return nil, fmt.Errorf("allocate operation timeline sequence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO operation_timeline (
			id, operation_id, sequence, entry_type, state_version, data, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, appended.ID, appended.OperationID, appended.Sequence, appended.Kind,
		appended.OperationStateVersion, []byte(appended.Data), appended.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		if isUniqueConstraint(err) {
			return nil, store.ErrDuplicateKey
		}
		return nil, fmt.Errorf("insert operation timeline entry: %w", err)
	}
	return &appended, nil
}

func (s *timelineStore) List(ctx context.Context, operationID string, afterSequence, throughSequence int64) ([]*store.OperationTimelineEntry, error) {
	query := `
		SELECT id, operation_id, sequence, entry_type, state_version, data, created_at
		FROM operation_timeline
		WHERE operation_id = ? AND sequence > ?`
	args := []any{operationID, afterSequence}
	if throughSequence > 0 {
		query += ` AND sequence <= ?`
		args = append(args, throughSequence)
	}
	query += ` ORDER BY sequence ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list operation timeline entries: %w", err)
	}
	defer rows.Close()
	entries := make([]*store.OperationTimelineEntry, 0)
	for rows.Next() {
		entry, scanErr := scanTimelineEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operation timeline entries: %w", err)
	}
	return entries, nil
}

func (s *timelineStore) LatestSequence(ctx context.Context, operationID string) (int64, error) {
	var sequence int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) FROM operation_timeline WHERE operation_id = ?
	`, operationID).Scan(&sequence); err != nil {
		return 0, fmt.Errorf("get latest operation timeline sequence: %w", err)
	}
	return sequence, nil
}

func (s *timelineStore) Snapshot(ctx context.Context, operationID string) (*store.TimelineSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin operation timeline snapshot: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only rollback
	op, err := getOperation(ctx, tx, operationID)
	if err != nil {
		return nil, err
	}
	var latest, retained sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT MAX(sequence), MIN(sequence) FROM operation_timeline WHERE operation_id = ?
	`, operationID).Scan(&latest, &retained); err != nil {
		return nil, fmt.Errorf("get operation timeline snapshot boundary: %w", err)
	}
	snapshot := &store.TimelineSnapshot{Operation: op}
	if latest.Valid {
		snapshot.SnapshotSequence = latest.Int64
	}
	if retained.Valid {
		snapshot.RetainedFromSequence = retained.Int64
	}
	return snapshot, nil
}

func scanTimelineEntry(row interface{ Scan(...any) error }) (*store.OperationTimelineEntry, error) {
	var entry store.OperationTimelineEntry
	var data []byte
	var createdAt string
	if err := row.Scan(&entry.ID, &entry.OperationID, &entry.Sequence, &entry.Kind,
		&entry.OperationStateVersion, &data, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan operation timeline entry: %w", err)
	}
	entry.Data = append(json.RawMessage(nil), data...)
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse operation timeline created_at: %w", err)
	}
	entry.CreatedAt = parsed.UTC()
	return &entry, nil
}
