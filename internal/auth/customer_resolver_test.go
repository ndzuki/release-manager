package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestStoreCustomerResolver_ResolveAndNotFound(t *testing.T) {
	st, err := sqlitestore.Open("file:" + uuid.New().String() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	resolver := auth.NewStoreCustomerResolver(st)
	if _, err := resolver.Resolve(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown customer: err = %v, want store.ErrNotFound", err)
	}

	customer := &store.Customer{ID: "c-1", Name: "Acme", Slug: "acme", Status: store.CustomerDisabled}
	if err := createCustomerViaManagement(ctx, st, customer); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	got, err := resolver.Resolve(ctx, "c-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Status != store.CustomerDisabled {
		t.Fatalf("status = %q, want %q", got.Status, store.CustomerDisabled)
	}
}

// createCustomerViaManagement creates a customer through the canonical atomic seam
// (customer + its active organization binding commit together); the standalone
// Customers().Create had no shipping caller (TASK-226). The synthetic organization is
// derived from the customer id, so fixtures that manage their own organizations and
// bindings are not perturbed.
func createCustomerViaManagement(ctx context.Context, st interface {
	Organizations() store.OrganizationStore
	CustomerCreates() store.CustomerBindingCreateStore
}, customer *store.Customer) error {
	orgID := "org-managed:" + customer.ID
	if err := st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: orgID}); err != nil {
		// A synthetic organization created by an earlier fixture of the same test is fine.
		if existing, getErr := st.Organizations().Get(ctx, orgID); getErr != nil || existing == nil {
			return err
		}
	}
	return st.CustomerCreates().CreateCustomerWithOrgBinding(ctx, store.CustomerBindingCreateCommand{
		Customer:  customer,
		OrgID:     orgID,
		BindingID: "binding-managed:" + customer.ID,
	})
}
