package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type organizationMemberStore struct{ db *sql.DB }

//nolint:dupl // Organization and membership stores intentionally share timestamped CRUD structure.
func (s *organizationMemberStore) Create(ctx context.Context, m *store.OrganizationMember) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = m.CreatedAt
	}
	if m.Role == "" {
		m.Role = store.RoleViewer
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO organization_members (org_id, user_id, role, optimistic_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		m.OrgID, m.UserID, string(m.Role), m.OptimisticVersion,
		m.CreatedAt.UTC().Format(time.RFC3339), m.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert organization member: %w", err)
	}
	return nil
}

func (s *organizationMemberStore) Get(ctx context.Context, orgID, userID string) (*store.OrganizationMember, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT org_id, user_id, role, optimistic_version, created_at, updated_at
		FROM organization_members WHERE org_id = ? AND user_id = ?`, orgID, userID)
	return scanOrganizationMember(row)
}

func (s *organizationMemberStore) ListByOrg(ctx context.Context, orgID string) ([]*store.OrganizationMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT org_id, user_id, role, optimistic_version, created_at, updated_at
		FROM organization_members WHERE org_id = ? ORDER BY created_at`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list members by org: %w", err)
	}
	defer rows.Close()

	var members []*store.OrganizationMember
	for rows.Next() {
		m, err := scanOrganizationMember(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (s *organizationMemberStore) ListByUser(ctx context.Context, userID string) ([]*store.OrganizationMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT org_id, user_id, role, optimistic_version, created_at, updated_at
		FROM organization_members WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("list members by user: %w", err)
	}
	defer rows.Close()

	var members []*store.OrganizationMember
	for rows.Next() {
		m, err := scanOrganizationMember(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (s *organizationMemberStore) Update(ctx context.Context, m *store.OrganizationMember) error {
	m.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE organization_members
		SET role = ?, optimistic_version = optimistic_version + 1, updated_at = ?
		WHERE org_id = ? AND user_id = ? AND optimistic_version = ?`,
		string(m.Role), m.UpdatedAt.UTC().Format(time.RFC3339),
		m.OrgID, m.UserID, m.OptimisticVersion,
	)
	if err != nil {
		return fmt.Errorf("update organization member: %w", err)
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

func (s *organizationMemberStore) Delete(ctx context.Context, orgID, userID string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM organization_members WHERE org_id = ? AND user_id = ?`, orgID, userID)
	if err != nil {
		return fmt.Errorf("delete organization member: %w", err)
	}
	return nil
}

//nolint:dupl // Organization and membership scanners intentionally share status/time decoding structure.
func scanOrganizationMember(row interface{ Scan(...interface{}) error }) (*store.OrganizationMember, error) {
	var (
		m            store.OrganizationMember
		role         string
		createdAtStr string
		updatedAtStr string
	)
	if err := row.Scan(&m.OrgID, &m.UserID, &role, &m.OptimisticVersion,
		&createdAtStr, &updatedAtStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan organization member: %w", err)
	}
	m.Role = store.Role(role)
	createdAt, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse org member created_at: %w", err)
	}
	m.CreatedAt = createdAt
	updatedAt, err := time.Parse(time.RFC3339, updatedAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse org member updated_at: %w", err)
	}
	m.UpdatedAt = updatedAt
	return &m, nil
}
