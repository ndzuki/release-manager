package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type valuesStore struct{ db *sql.DB }

func (s *valuesStore) Create(ctx context.Context, vr *store.ValuesRevision) error {
	if vr.CreatedAt.IsZero() {
		vr.CreatedAt = time.Now().UTC()
	}
	if vr.UpdatedAt.IsZero() {
		vr.UpdatedAt = vr.CreatedAt
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO values_revisions (
			id, release_definition_id, revision, status, "values",
			digest, parent_revision_id, secret_refs,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		vr.ID, vr.ReleaseDefinitionID, vr.Revision, string(vr.Status), vr.Values,
		vr.Digest, vr.ParentRevisionID, vr.SecretRefs,
		vr.CreatedAt.UTC().Format(time.RFC3339), vr.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert values revision: %w", err)
	}
	return nil
}

func (s *valuesStore) Get(ctx context.Context, id string) (*store.ValuesRevision, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, release_definition_id, revision, status, "values",
			digest, parent_revision_id, secret_refs,
			created_at, updated_at
		FROM values_revisions WHERE id = ?
	`, id)
	return scanValues(row)
}

func (s *valuesStore) GetByDigest(ctx context.Context, definitionID, digest string) (*store.ValuesRevision, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, release_definition_id, revision, status, "values",
			digest, parent_revision_id, secret_refs,
			created_at, updated_at
		FROM values_revisions
		WHERE release_definition_id = ? AND digest = ?
	`, definitionID, digest)
	return scanValues(row)
}

func (s *valuesStore) GetLatestApproved(ctx context.Context, definitionID string) (*store.ValuesRevision, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, release_definition_id, revision, status, "values",
			digest, parent_revision_id, secret_refs,
			created_at, updated_at
		FROM values_revisions
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, release_definition_id, revision, status, "values",
			digest, parent_revision_id, secret_refs,
			created_at, updated_at
		FROM values_revisions
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

// Update persists status changes with optimistic locking on parent_revision_id.
func (s *valuesStore) Update(ctx context.Context, vr *store.ValuesRevision, expectedParentRev string) error {
	vr.UpdatedAt = time.Now().UTC()

	result, err := s.db.ExecContext(ctx, `
		UPDATE values_revisions
		SET status = ?,
		    digest = ?,
		    secret_refs = ?,
		    updated_at = ?
		WHERE id = ? AND parent_revision_id = ?
	`, string(vr.Status), vr.Digest, vr.SecretRefs,
		vr.UpdatedAt.UTC().Format(time.RFC3339),
		vr.ID, expectedParentRev)
	if err != nil {
		return fmt.Errorf("update values revision: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update values revision rows: %w", err)
	}
	if n == 0 {
		return store.ErrOptimisticLock
	}
	return nil
}

func scanValues(row interface{ Scan(...interface{}) error }) (*store.ValuesRevision, error) {
	var (
		id, defID, status        string
		revision                 int
		values                   []byte
		digest, parentRevisionID string
		secretRefs               []byte
		createdAt, updatedAt     string
	)

	err := row.Scan(&id, &defID, &revision, &status, &values,
		&digest, &parentRevisionID, &secretRefs,
		&createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan values: %w", err)
	}

	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}

	return &store.ValuesRevision{
		ID:                  id,
		ReleaseDefinitionID: defID,
		Revision:            revision,
		Status:              store.ValuesStatus(status),
		Values:              values,
		Digest:              digest,
		ParentRevisionID:    parentRevisionID,
		SecretRefs:          secretRefs,
		CreatedAt:           ct,
		UpdatedAt:           ut,
	}, nil
}
