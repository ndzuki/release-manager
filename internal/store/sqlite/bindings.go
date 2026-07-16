package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type bindingStore struct{ db *sql.DB }

func (s *bindingStore) Create(ctx context.Context, b *store.OrgCustomerBinding) error {
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}
	if b.UpdatedAt.IsZero() {
		b.UpdatedAt = b.CreatedAt
	}
	if b.Status == "" {
		b.Status = store.BindingActive
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO org_customer_bindings (id, org_id, customer_id, status, optimistic_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.OrgID, b.CustomerID, string(b.Status), b.OptimisticVersion,
		b.CreatedAt.UTC().Format(time.RFC3339), b.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert binding: %w", err)
	}
	return nil
}

func (s *bindingStore) Get(ctx context.Context, id string) (*store.OrgCustomerBinding, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, org_id, customer_id, status, optimistic_version, created_at, updated_at
		FROM org_customer_bindings WHERE id = ?`, id)
	return scanBinding(row)
}

func (s *bindingStore) GetByOrgAndCustomer(ctx context.Context, orgID, customerID string) (*store.OrgCustomerBinding, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, org_id, customer_id, status, optimistic_version, created_at, updated_at
		FROM org_customer_bindings WHERE org_id = ? AND customer_id = ?`, orgID, customerID)
	return scanBinding(row)
}

func (s *bindingStore) ListByOrg(ctx context.Context, orgID string) ([]*store.OrgCustomerBinding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, org_id, customer_id, status, optimistic_version, created_at, updated_at
		FROM org_customer_bindings WHERE org_id = ? ORDER BY created_at`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list bindings by org: %w", err)
	}
	defer rows.Close()

	var bindings []*store.OrgCustomerBinding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	return bindings, rows.Err()
}

func (s *bindingStore) Update(ctx context.Context, b *store.OrgCustomerBinding) error {
	b.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE org_customer_bindings
		SET status = ?, optimistic_version = optimistic_version + 1, updated_at = ?
		WHERE id = ? AND optimistic_version = ?`,
		string(b.Status), b.UpdatedAt.UTC().Format(time.RFC3339),
		b.ID, b.OptimisticVersion,
	)
	if err != nil {
		return fmt.Errorf("update binding: %w", err)
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

func scanBinding(row interface{ Scan(...interface{}) error }) (*store.OrgCustomerBinding, error) {
	var (
		b            store.OrgCustomerBinding
		status       string
		createdAtStr string
		updatedAtStr string
	)
	if err := row.Scan(&b.ID, &b.OrgID, &b.CustomerID, &status, &b.OptimisticVersion,
		&createdAtStr, &updatedAtStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan binding: %w", err)
	}
	b.Status = store.BindingStatus(status)
	createdAt, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse binding created_at: %w", err)
	}
	b.CreatedAt = createdAt
	updatedAt, err := time.Parse(time.RFC3339, updatedAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse binding updated_at: %w", err)
	}
	b.UpdatedAt = updatedAt
	return &b, nil
}
