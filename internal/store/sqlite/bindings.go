package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/store"
)

type bindingStore struct{ db *sql.DB }

func (s *bindingStore) Create(ctx context.Context, binding *store.OrgCustomerBinding) error {
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = time.Now().UTC()
	}
	if binding.UpdatedAt.IsZero() {
		binding.UpdatedAt = binding.CreatedAt
	}
	if binding.Status == "" {
		binding.Status = store.BindingActive
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create binding: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.

	_, err = tx.ExecContext(ctx, `
		INSERT INTO org_customer_bindings (id, org_id, customer_id, status, optimistic_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		binding.ID, binding.OrgID, binding.CustomerID, string(binding.Status), binding.OptimisticVersion,
		binding.CreatedAt.UTC().Format(time.RFC3339), binding.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrDuplicateKey
		}
		return fmt.Errorf("insert binding: %w", err)
	}
	if err := insertBindingEvent(ctx, tx, binding); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create binding: %w", err)
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
		binding, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

func (s *bindingStore) SetStatus(ctx context.Context, id string, status store.BindingStatus) error {
	_, err := s.db.ExecContext(ctx, `UPDATE org_customer_bindings SET status = ?, updated_at = ? WHERE id = ?`,
		string(status), time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (s *bindingStore) RequireActive(ctx context.Context, orgID, customerID string) error {
	var status string
	err := s.db.QueryRowContext(ctx, `
		SELECT status FROM org_customer_bindings
		WHERE org_id = ? AND customer_id = ?`, orgID, customerID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		return fmt.Errorf("require active binding: %w", err)
	}
	if store.BindingStatus(status) != store.BindingActive {
		return store.ErrBindingRevoked
	}
	return nil
}

func insertBindingEvent(ctx context.Context, tx *sql.Tx, binding *store.OrgCustomerBinding) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO organization_customer_binding_events (
			id, binding_id, org_id, customer_id, status, optimistic_version, changed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uuid.New().String(),
		binding.ID,
		binding.OrgID,
		binding.CustomerID,
		string(binding.Status),
		binding.OptimisticVersion,
		binding.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert binding event: %w", err)
	}
	return nil
}

func scanBinding(row interface{ Scan(...interface{}) error }) (*store.OrgCustomerBinding, error) {
	var (
		binding   store.OrgCustomerBinding
		status    string
		createdAt string
		updatedAt string
	)
	if err := row.Scan(
		&binding.ID,
		&binding.OrgID,
		&binding.CustomerID,
		&status,
		&binding.OptimisticVersion,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan binding: %w", err)
	}
	binding.Status = store.BindingStatus(status)
	parsedCreatedAt, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse binding created_at: %w", err)
	}
	binding.CreatedAt = parsedCreatedAt
	parsedUpdatedAt, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse binding updated_at: %w", err)
	}
	binding.UpdatedAt = parsedUpdatedAt
	return &binding, nil
}

func (s *bindingStore) ListByCustomer(ctx context.Context, customerID string) ([]*store.OrgCustomerBinding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, organization_id, customer_id, status, created_at, updated_at
		FROM org_customer_bindings WHERE customer_id = ?`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []*store.OrgCustomerBinding
	for rows.Next() {
		b := &store.OrgCustomerBinding{}
		if err := rows.Scan(&b.ID, &b.OrgID, &b.CustomerID, &b.Status, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	return bindings, rows.Err()
}
