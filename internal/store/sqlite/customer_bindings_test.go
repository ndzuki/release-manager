package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// Prototype Gate (plan v3 Step 1, risk: high): the atomic customer+binding
// creation seam and the Customer CAS contract on the SQLite adapter.

func TestCreateCustomerWithOrgBinding_CommitsAtomically(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()

	org := &store.Organization{ID: "org-atomic", Name: "Atomic Org"}
	require.NoError(t, st.Organizations().Create(ctx, org))

	customer := &store.Customer{ID: "cust-atomic", Name: "Atomic Co", Slug: "atomic-co"}
	require.NoError(t, st.CustomerCreates().CreateCustomerWithOrgBinding(ctx,
		store.CustomerBindingCreateCommand{
			Customer:  customer,
			OrgID:     org.ID,
			BindingID: "binding-atomic",
		}))

	// Customer row is visible.
	got, err := st.Customers().Get(ctx, customer.ID)
	require.NoError(t, err)
	assert.Equal(t, store.CustomerActive, got.Status)
	assert.EqualValues(t, 1, got.Version)

	// Active binding is visible and authoritative.
	require.NoError(t, st.Bindings().RequireActive(ctx, org.ID, customer.ID))

	// Binding event and authorization source version are persisted.
	// The binding rows are read directly: BindingStore.ListByCustomer was removed with the
	// TASK-226 dead-surface batch (no shipping reader), so the fixture reads what it asserts.
	bindingRows, err := st.DB().QueryContext(ctx,
		`SELECT id FROM org_customer_bindings WHERE customer_id = ?`, customer.ID)
	require.NoError(t, err)
	var bindingIDs []string
	for bindingRows.Next() {
		var id string
		require.NoError(t, bindingRows.Scan(&id))
		bindingIDs = append(bindingIDs, id)
	}
	defer bindingRows.Close() //nolint:errcheck // test fixture read
	require.NoError(t, bindingRows.Err())
	require.Len(t, bindingIDs, 1)

	var eventCount int
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM organization_customer_binding_events WHERE binding_id = ?`, bindingIDs[0],
	).Scan(&eventCount))
	assert.Equal(t, 1, eventCount)

	var sourceVersion int64
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT version FROM authorization_source_version WHERE id = 1`,
	).Scan(&sourceVersion))
	assert.GreaterOrEqual(t, sourceVersion, int64(1))
}

func TestCreateCustomerWithOrgBinding_RollsBackOnFailure(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()

	org := &store.Organization{ID: "org-rollback", Name: "Rollback Org"}
	require.NoError(t, st.Organizations().Create(ctx, org))

	// Seed a conflicting customer so the atomic insert fails mid-transaction. The row is
	// inserted directly on purpose: the canonical seam always writes an organization
	// binding and bumps the authorization source version, and this fixture must start
	// from neither (Customers().Create was the binding-free insert removed in TASK-226).
	seed := &store.Customer{ID: "cust-dup", Name: "Dup", Slug: "dup"}
	now := time.Now().UTC().Format(time.RFC3339)
	_, seedErr := st.DB().ExecContext(ctx,
		`INSERT INTO customers (id, name, slug, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		seed.ID, seed.Name, seed.Slug, string(store.CustomerActive), now, now)
	require.NoError(t, seedErr)

	conflict := &store.Customer{ID: "cust-dup", Name: "Conflicting", Slug: "conflicting"}
	err := st.CustomerCreates().CreateCustomerWithOrgBinding(ctx,
		store.CustomerBindingCreateCommand{
			Customer:  conflict,
			OrgID:     org.ID,
			BindingID: "binding-rollback",
		})
	require.Error(t, err)

	// The binding insert must not have leaked: no binding, no event, no bump.
	_, err = st.Bindings().GetByOrgAndCustomer(ctx, org.ID, seed.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)

	bindings, listErr := st.Bindings().ListByOrg(ctx, org.ID)
	require.NoError(t, listErr)
	assert.Empty(t, bindings)

	var sourceVersion int64
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT version FROM authorization_source_version WHERE id = 1`,
	).Scan(&sourceVersion))
	assert.Zero(t, sourceVersion)
}

func TestCustomerUpdate_CASConflict(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()

	c := &store.Customer{ID: "cust-cas", Name: "CAS Co", Slug: "cas-co"}
	require.NoError(t, createCustomerViaManagement(ctx, st, c))
	require.EqualValues(t, 1, c.Version)

	// Two writers race on the same expected version: only one wins.
	first := &store.Customer{ID: c.ID, Name: "First Write", Slug: "cas-co"}
	require.NoError(t, st.Customers().Update(ctx, first, 1))

	second := &store.Customer{ID: c.ID, Name: "Second Write", Slug: "cas-co"}
	err := st.Customers().Update(ctx, second, 1)
	assert.ErrorIs(t, err, store.ErrOptimisticLock)

	// The winner's data is authoritative and the version advanced.
	got, err := st.Customers().Get(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "First Write", got.Name)
	assert.EqualValues(t, 2, got.Version)
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
