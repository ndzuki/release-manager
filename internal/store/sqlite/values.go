package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type valuesStore struct{ db *sql.DB }

func (s *valuesStore) Create(ctx context.Context, vr *store.ValuesRevision) error {
	if vr.StateVersion == 0 {
		vr.StateVersion = 1
	}
	if vr.CreatedAt.IsZero() {
		vr.CreatedAt = time.Now().UTC()
	}
	if vr.UpdatedAt.IsZero() {
		vr.UpdatedAt = vr.CreatedAt
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO values_revisions (
			id, release_definition_id, revision, version, state_version, status, "values",
			digest, parent_revision_id, secret_refs, created_by, created_by_user_id,
			approved_by, approved_at, rejected_by, rejection_reason, submitted_at, decided_at,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, '', '', ?, ?, ?, ?)
	`,
		vr.ID, vr.ReleaseDefinitionID, vr.Revision, vr.StateVersion, vr.StateVersion, string(vr.Status), vr.Values,
		vr.Digest, vr.ParentRevisionID, vr.SecretRefs, vr.CreatedByUserID, vr.CreatedByUserID,
		formatOptionalTime(vr.SubmittedAt), formatOptionalTime(vr.DecidedAt),
		vr.CreatedAt.UTC().Format(time.RFC3339Nano), vr.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert values revision: %w", err)
	}
	return nil
}

func (s *valuesStore) Get(ctx context.Context, id string) (*store.ValuesRevision, error) {
	row := s.db.QueryRowContext(ctx, valuesSelect+` WHERE id = ?`, id)
	return scanValues(row)
}

func (s *valuesStore) GetByDigest(ctx context.Context, definitionID, digest string) (*store.ValuesRevision, error) {
	row := s.db.QueryRowContext(ctx, valuesSelect+`
		WHERE release_definition_id = ? AND digest = ?
	`, definitionID, digest)
	return scanValues(row)
}

func (s *valuesStore) GetLatestApproved(ctx context.Context, definitionID string) (*store.ValuesRevision, error) {
	row := s.db.QueryRowContext(ctx, valuesSelect+`
		WHERE release_definition_id = ? AND status = 'approved'
		ORDER BY revision DESC
		LIMIT 1
	`, definitionID)
	vr, err := scanValues(row)
	if err != nil {
		return nil, err
	}
	return vr, nil
}

func (s *valuesStore) List(ctx context.Context, definitionID string) ([]*store.ValuesRevision, error) {
	rows, err := s.db.QueryContext(ctx, valuesSelect+`
		WHERE release_definition_id = ?
		ORDER BY revision DESC
	`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("list values revisions: %w", err)
	}
	defer rows.Close()

	var revs []*store.ValuesRevision
	for rows.Next() {
		vr, err := scanValues(rows)
		if err != nil {
			return nil, err
		}
		revs = append(revs, vr)
	}
	return revs, rows.Err()
}

// GetNextRevisionNumber returns max(revision)+1 for the given definition, or 1 if none exist.
func (s *valuesStore) GetNextRevisionNumber(ctx context.Context, definitionID string) (int, error) {
	var maxRev sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(revision) FROM values_revisions
		WHERE release_definition_id = ?
	`, definitionID).Scan(&maxRev)
	if err != nil {
		return 0, fmt.Errorf("get next revision number: %w", err)
	}
	if maxRev.Valid {
		return int(maxRev.Int64) + 1, nil
	}
	return 1, nil
}

const valuesSelect = `
	SELECT id, release_definition_id, revision, state_version, status, "values",
		digest, parent_revision_id, secret_refs, created_by_user_id,
		submitted_at, decided_at, created_at, updated_at
	FROM values_revisions`

func getValues(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (*store.ValuesRevision, error) {
	return scanValues(q.QueryRowContext(ctx, valuesSelect+` WHERE id = ?`, id))
}

func scanValues(row interface{ Scan(...interface{}) error }) (*store.ValuesRevision, error) {
	var (
		vr                     store.ValuesRevision
		status                 string
		submittedAt, decidedAt sql.NullString
		createdAt, updatedAt   string
	)

	err := row.Scan(
		&vr.ID, &vr.ReleaseDefinitionID, &vr.Revision, &vr.StateVersion, &status, &vr.Values,
		&vr.Digest, &vr.ParentRevisionID, &vr.SecretRefs, &vr.CreatedByUserID,
		&submittedAt, &decidedAt, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan values: %w", err)
	}

	vr.Status = store.ValuesStatus(status)
	var parseErr error
	if submittedAt.Valid {
		vr.SubmittedAt, parseErr = parseOptionalTime(submittedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse submitted_at: %w", parseErr)
		}
	}
	if decidedAt.Valid {
		vr.DecidedAt, parseErr = parseOptionalTime(decidedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse decided_at: %w", parseErr)
		}
	}
	vr.CreatedAt, parseErr = parseSQLiteTime(createdAt)
	if parseErr != nil {
		return nil, fmt.Errorf("parse created_at: %w", parseErr)
	}
	vr.UpdatedAt, parseErr = parseSQLiteTime(updatedAt)
	if parseErr != nil {
		return nil, fmt.Errorf("parse updated_at: %w", parseErr)
	}
	return &vr, nil
}

func parseOptionalTime(value string) (*time.Time, error) {
	parsed, err := parseSQLiteTime(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseSQLiteTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time value %q", value)
}

func formatOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
