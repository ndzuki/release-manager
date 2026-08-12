package postgres

import (
	"context"
	"fmt"

	"github.com/ndzuki/release-manager/internal/store"
)

// customerBindingCreateStore implements the atomic customer + org-binding
// creation seam (REQ-051 Step 1): the customer row, its active organization
// binding, the binding event, and the authorization source-version bump commit
// in a single transaction, so a created customer is never invisible to the
// organization that created it.
type customerBindingCreateStore struct{ gorm *DB }

func (s *customerBindingCreateStore) CreateCustomerWithOrgBinding(
	ctx context.Context,
	cmd store.CustomerBindingCreateCommand,
) error {
	if cmd.Customer == nil {
		return fmt.Errorf("create customer with binding: nil customer")
	}
	if cmd.OrgID == "" || cmd.BindingID == "" {
		return fmt.Errorf("create customer with binding: org id and binding id are required")
	}

	tx, err := s.gorm.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create customer with binding: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback after Commit is a no-op.

	if err := insertCustomer(ctx, tx, cmd.Customer); err != nil {
		return err
	}
	binding := &store.OrgCustomerBinding{
		ID:         cmd.BindingID,
		OrgID:      cmd.OrgID,
		CustomerID: cmd.Customer.ID,
	}
	if err := createBindingInTx(ctx, tx, binding); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create customer with binding: %w", err)
	}
	return nil
}
