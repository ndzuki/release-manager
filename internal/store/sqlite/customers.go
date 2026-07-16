package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type customerStore struct{ db *sql.DB }

func (s *customerStore) Create(ctx context.Context, c *store.Customer) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = c.CreatedAt
	}
	if c.Status == "" {
		c.Status = store.CustomerActive
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO customers (id, name, slug, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
`,
		c.ID, c.Name, c.Slug, string(c.Status),
		c.CreatedAt.UTC().Format(time.RFC3339), c.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert customer: %w", err)
	}
	return nil
}

func (s *customerStore) Get(ctx context.Context, id string) (*store.Customer, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, slug, status, created_at, updated_at
FROM customers WHERE id = ?
`, id)
	return scanCustomer(row)
}

func (s *customerStore) GetBySlug(ctx context.Context, slug string) (*store.Customer, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, slug, status, created_at, updated_at
FROM customers WHERE slug = ?
`, slug)
	return scanCustomer(row)
}

func (s *customerStore) Update(ctx context.Context, c *store.Customer) error {
	c.UpdatedAt = time.Now().UTC()

	_, err := s.db.ExecContext(ctx, `
UPDATE customers SET name=?, slug=?, status=?, updated_at=?
WHERE id=?
`,
		c.Name, c.Slug, string(c.Status),
		c.UpdatedAt.UTC().Format(time.RFC3339), c.ID,
	)
	if err != nil {
		return fmt.Errorf("update customer: %w", err)
	}
	return nil
}

func (s *customerStore) List(ctx context.Context, includeDisabled bool) ([]*store.Customer, error) {
	query := `SELECT id, name, slug, status, created_at, updated_at
FROM customers`
	if !includeDisabled {
		query += ` WHERE status = 'active'`
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query)
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
		createdAt, updatedAt   string
	)
	if err := row.Scan(&id, &name, &slug, &status, &createdAt, &updatedAt); err != nil {
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
		CreatedAt: ct,
		UpdatedAt: ut,
	}, nil
}

func scanCustomerFromRows(rows *sql.Rows) (*store.Customer, error) {
	var (
		id, name, slug, status string
		createdAt, updatedAt   string
	)
	if err := rows.Scan(&id, &name, &slug, &status, &createdAt, &updatedAt); err != nil {
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
		CreatedAt: ct,
		UpdatedAt: ut,
	}, nil
}
