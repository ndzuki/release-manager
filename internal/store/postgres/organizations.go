package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type organizationStore struct{ gorm *DB }

//nolint:dupl // Organization SQL remains explicit beside the distinct member contract.
func (s *organizationStore) Create(ctx context.Context, o *store.Organization) error {
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	if o.UpdatedAt.IsZero() {
		o.UpdatedAt = o.CreatedAt
	}
	if o.Status == "" {
		o.Status = store.OrgActive
	}

	_, err := s.gorm.ExecContext(ctx, `
		INSERT INTO organizations (id, name, status, optimistic_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		o.ID, o.Name, string(o.Status), o.OptimisticVersion,
		o.CreatedAt.UTC().Format(time.RFC3339), o.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert organization: %w", err)
	}
	return nil
}

func (s *organizationStore) Get(ctx context.Context, id string) (*store.Organization, error) {
	row := s.gorm.QueryRowContext(ctx, `
		SELECT id, name, status, optimistic_version, created_at, updated_at
		FROM organizations WHERE id = ?`, id)
	return scanOrganization(row)
}

func (s *organizationStore) List(ctx context.Context) ([]*store.Organization, error) {
	rows, err := s.gorm.QueryContext(ctx, `
		SELECT id, name, status, optimistic_version, created_at, updated_at
		FROM organizations ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	defer rows.Close()

	var orgs []*store.Organization
	for rows.Next() {
		o, err := scanOrganization(rows)
		if err != nil {
			return nil, err
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

func (s *organizationStore) Update(ctx context.Context, o *store.Organization) error {
	o.UpdatedAt = time.Now().UTC()
	result, err := s.gorm.ExecContext(ctx, `
		UPDATE organizations
		SET name = ?, status = ?, optimistic_version = optimistic_version + 1, updated_at = ?
		WHERE id = ? AND optimistic_version = ?`,
		o.Name, string(o.Status), o.UpdatedAt.UTC().Format(time.RFC3339),
		o.ID, o.OptimisticVersion,
	)
	if err != nil {
		return fmt.Errorf("update organization: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrOptimisticLock
	}
	return nil
}

//nolint:dupl // Organization scanning names its domain fields and errors explicitly.
func scanOrganization(row interface{ Scan(...interface{}) error }) (*store.Organization, error) {
	var (
		o            store.Organization
		status       string
		createdAtStr string
		updatedAtStr string
	)
	if err := row.Scan(&o.ID, &o.Name, &status, &o.OptimisticVersion,
		&createdAtStr, &updatedAtStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan organization: %w", err)
	}
	o.Status = store.OrganizationStatus(status)
	createdAt, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse org created_at: %w", err)
	}
	o.CreatedAt = createdAt
	updatedAt, err := time.Parse(time.RFC3339, updatedAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse org updated_at: %w", err)
	}
	o.UpdatedAt = updatedAt
	return &o, nil
}
