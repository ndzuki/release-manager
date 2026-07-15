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
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		vr.ID, vr.ReleaseDefinitionID, vr.Revision, string(vr.Status), vr.Values,
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
			created_at, updated_at
		FROM values_revisions WHERE id = ?
	`, id)
	return scanValues(row)
}

func (s *valuesStore) GetLatestApproved(ctx context.Context, definitionID string) (*store.ValuesRevision, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, release_definition_id, revision, status, "values",
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

func scanValues(row interface{ Scan(...interface{}) error }) (*store.ValuesRevision, error) {
	var (
		id, defID, status string
		revision          int
		values            []byte
		createdAt, updatedAt string
	)

	err := row.Scan(&id, &defID, &revision, &status, &values, &createdAt, &updatedAt)
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
		CreatedAt:           ct,
		UpdatedAt:           ut,
	}, nil
}
