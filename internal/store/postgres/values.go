package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type valuesStore struct{ gorm *DB }

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

	_, err := s.gorm.ExecContext(ctx, `
		INSERT INTO values_revisions (
			id, release_definition_id, revision, version, state_version, status, "values",
			digest, parent_revision_id, secret_refs, created_by, created_by_user_id,
			approved_by, approved_at, rejected_by, rejection_reason, submitted_at, decided_at,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, '', '', ?, ?, ?, ?)
	`,
		vr.ID, vr.ReleaseDefinitionID, vr.Revision, vr.StateVersion, vr.StateVersion, string(vr.Status), vr.Values,
		vr.Digest, vr.ParentRevisionID, vr.SecretRefs, vr.CreatedByUserID, vr.CreatedByUserID,
		valuesOptionalTime(vr.SubmittedAt), valuesOptionalTime(vr.DecidedAt),
		vr.CreatedAt.UTC(), vr.UpdatedAt.UTC(),
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
	return scanValues(s.gorm.QueryRowContext(ctx, valuesSelect+` WHERE id = ?`, id))
}

func (s *valuesStore) GetByDigest(ctx context.Context, definitionID, digest string) (*store.ValuesRevision, error) {
	return scanValues(s.gorm.QueryRowContext(ctx, valuesSelect+`
		WHERE release_definition_id = ? AND digest = ?
	`, definitionID, digest))
}

func (s *valuesStore) GetLatestApproved(ctx context.Context, definitionID string) (*store.ValuesRevision, error) {
	return scanValues(s.gorm.QueryRowContext(ctx, valuesSelect+`
		WHERE release_definition_id = ? AND status = 'approved'
		ORDER BY revision DESC
		LIMIT 1
	`, definitionID))
}

func (s *valuesStore) List(ctx context.Context, definitionID string) ([]*store.ValuesRevision, error) {
	rows, err := s.gorm.QueryContext(ctx, valuesSelect+`
		WHERE release_definition_id = ?
		ORDER BY revision DESC
	`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("list values revisions: %w", err)
	}
	defer rows.Close()

	var revisions []*store.ValuesRevision
	for rows.Next() {
		revision, err := scanValues(rows)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate values revisions: %w", err)
	}
	return revisions, nil
}

// GetNextRevisionNumber returns max(revision)+1 for the given definition, or 1 if none exist.
func (s *valuesStore) GetNextRevisionNumber(ctx context.Context, definitionID string) (int, error) {
	var maxRevision sql.NullInt64
	if err := s.gorm.QueryRowContext(ctx, `
		SELECT MAX(revision) FROM values_revisions
		WHERE release_definition_id = ?
	`, definitionID).Scan(&maxRevision); err != nil {
		return 0, fmt.Errorf("get next revision number: %w", err)
	}
	if maxRevision.Valid {
		return int(maxRevision.Int64) + 1, nil
	}
	return 1, nil
}

const valuesSelect = `
	SELECT id, release_definition_id, revision, state_version, status, "values",
		digest, parent_revision_id, secret_refs, created_by_user_id,
		submitted_at, decided_at, created_at, updated_at
	FROM values_revisions`

func scanValues(row interface{ Scan(...interface{}) error }) (*store.ValuesRevision, error) {
	var revision store.ValuesRevision
	var status string
	var submittedAt, decidedAt sql.NullTime
	if err := row.Scan(
		&revision.ID,
		&revision.ReleaseDefinitionID,
		&revision.Revision,
		&revision.StateVersion,
		&status,
		&revision.Values,
		&revision.Digest,
		&revision.ParentRevisionID,
		&revision.SecretRefs,
		&revision.CreatedByUserID,
		&submittedAt,
		&decidedAt,
		&revision.CreatedAt,
		&revision.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan values: %w", err)
	}
	revision.Status = store.ValuesStatus(status)
	revision.CreatedAt = revision.CreatedAt.UTC()
	revision.UpdatedAt = revision.UpdatedAt.UTC()
	if submittedAt.Valid {
		value := submittedAt.Time.UTC()
		revision.SubmittedAt = &value
	}
	if decidedAt.Valid {
		value := decidedAt.Time.UTC()
		revision.DecidedAt = &value
	}
	return &revision, nil
}

func valuesOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}
