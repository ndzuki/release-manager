package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type customerStore struct{ gorm *DB }

// insertCustomer persists one customer row, defaulting lifecycle fields and
// assigning the initial optimistic-lock version. execer accepts both *DB and
// *Tx so the write can participate in an outer transaction.
func insertCustomer(ctx context.Context, execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, c *store.Customer) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = c.CreatedAt
	}
	if c.Status == "" {
		c.Status = store.CustomerActive
	}

	_, err := execer.ExecContext(ctx, `
INSERT INTO customers (id, name, slug, status, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`,
		c.ID, c.Name, c.Slug, string(c.Status), 1,
		c.CreatedAt.UTC().Format(time.RFC3339), c.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert customer: %w", err)
	}
	c.Version = 1
	return nil
}

func (s *customerStore) Create(ctx context.Context, c *store.Customer) error {
	return insertCustomer(ctx, s.gorm, c)
}

func (s *customerStore) Get(ctx context.Context, id string) (*store.Customer, error) {
	row := s.gorm.QueryRowContext(ctx, `
SELECT id, name, slug, status, version, created_at, updated_at
FROM customers WHERE id = ?
`, id)
	return scanCustomer(row)
}

func (s *customerStore) GetBySlug(ctx context.Context, slug string) (*store.Customer, error) {
	row := s.gorm.QueryRowContext(ctx, `
SELECT id, name, slug, status, version, created_at, updated_at
FROM customers WHERE slug = ?
`, slug)
	return scanCustomer(row)
}

// Update applies changes with optimistic locking (AC-051-02): the stored
// version must equal expectedVersion, otherwise ErrOptimisticLock is returned
// and no row is modified.
//
//nolint:dupl // Customer and Cluster stores share the CAS update pattern.
func (s *customerStore) Update(ctx context.Context, c *store.Customer, expectedVersion int64) error {
	c.UpdatedAt = time.Now().UTC()

	result, err := s.gorm.ExecContext(ctx, `
UPDATE customers SET name=?, slug=?, status=?, updated_at=?, version=version+1
WHERE id=? AND version=?
`,
		c.Name, c.Slug, string(c.Status),
		c.UpdatedAt.UTC().Format(time.RFC3339), c.ID, expectedVersion,
	)
	if err != nil {
		return fmt.Errorf("update customer: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("customer update rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return store.ErrOptimisticLock
	}
	c.Version = expectedVersion + 1
	return nil
}

func (s *customerStore) List(ctx context.Context, includeDisabled bool) ([]*store.Customer, error) {
	query := `SELECT id, name, slug, status, version, created_at, updated_at
FROM customers`
	if !includeDisabled {
		query += ` WHERE status = 'active'`
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.gorm.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list customers: %w", err)
	}
	defer rows.Close()

	var customers []*store.Customer
	for rows.Next() {
		c, err := scanCustomerFromRows(rows)
		if err != nil {
			return nil, err
		}
		customers = append(customers, c)
	}
	return customers, rows.Err()
}

func scanCustomer(row interface{ Scan(...interface{}) error }) (*store.Customer, error) {
	var (
		id, name, slug, status string
		version                int64
		createdAt, updatedAt   string
	)
	if err := row.Scan(&id, &name, &slug, &status, &version, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan customer: %w", err)
	}

	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse customer created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse customer updated_at: %w", err)
	}

	return &store.Customer{
		ID:        id,
		Name:      name,
		Slug:      slug,
		Status:    store.CustomerStatus(status),
		Version:   version,
		CreatedAt: ct,
		UpdatedAt: ut,
	}, nil
}

func scanCustomerFromRows(rows *sql.Rows) (*store.Customer, error) {
	var (
		id, name, slug, status string
		version                int64
		createdAt, updatedAt   string
	)
	if err := rows.Scan(&id, &name, &slug, &status, &version, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("scan customer row: %w", err)
	}

	ct, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse customer created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse customer updated_at: %w", err)
	}

	return &store.Customer{
		ID:        id,
		Name:      name,
		Slug:      slug,
		Status:    store.CustomerStatus(status),
		Version:   version,
		CreatedAt: ct,
		UpdatedAt: ut,
	}, nil
}
