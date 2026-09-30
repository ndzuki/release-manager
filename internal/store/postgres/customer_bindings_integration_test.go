//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// Prototype Gate (plan v3 Step 1, risk: high): the atomic customer+binding
// creation seam and the Customer CAS contract on the PostgreSQL adapter.

func TestCreateCustomerWithOrgBinding_CommitsAtomically_Postgres(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	org := &store.Organization{ID: "org-atomic-pg", Name: "Atomic Org PG"}
	require.NoError(t, st.Organizations().Create(ctx, org))

	customer := &store.Customer{ID: "cust-atomic-pg", Name: "Atomic Co PG", Slug: "atomic-co-pg"}
	require.NoError(t, st.CustomerCreates().CreateCustomerWithOrgBinding(ctx,
		store.CustomerBindingCreateCommand{
			Customer:  customer,
			OrgID:     org.ID,
			BindingID: "binding-atomic-pg",
		}))

	got, err := st.Customers().Get(ctx, customer.ID)
	require.NoError(t, err)
	assert.Equal(t, store.CustomerActive, got.Status)
	assert.EqualValues(t, 1, got.Version)

	require.NoError(t, st.Bindings().RequireActive(ctx, org.ID, customer.ID))

	bindings, err := st.Bindings().ListByCustomer(ctx, customer.ID)
	require.NoError(t, err)
	require.Len(t, bindings, 1)

	var eventCount int
	require.NoError(t, st.SQLDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM organization_customer_binding_events WHERE binding_id = $1`, bindings[0].ID,
	).Scan(&eventCount))
	assert.Equal(t, 1, eventCount)

	var sourceVersion int64
	require.NoError(t, st.SQLDB().QueryRowContext(ctx,
		`SELECT version FROM authorization_source_version WHERE id = TRUE`,
	).Scan(&sourceVersion))
	assert.GreaterOrEqual(t, sourceVersion, int64(1))
}

func TestCreateCustomerWithOrgBinding_RollsBackOnFailure_Postgres(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	org := &store.Organization{ID: "org-rollback-pg", Name: "Rollback Org PG"}
	require.NoError(t, st.Organizations().Create(ctx, org))

	// Seed a conflicting customer so the atomic insert fails mid-transaction. The row is
	// inserted directly on purpose: the canonical seam always writes an organization
	// binding and bumps the authorization source version, and this fixture must start
	// from neither (Customers().Create was the binding-free insert removed in TASK-226).
	seed := &store.Customer{ID: "cust-dup-pg", Name: "Dup PG", Slug: "dup-pg"}
	now := time.Now().UTC()
	_, seedErr := st.SQLDB().ExecContext(ctx,
		`INSERT INTO customers (id, name, slug, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		seed.ID, seed.Name, seed.Slug, string(store.CustomerActive), now, now)
	require.NoError(t, seedErr)

	conflict := &store.Customer{ID: "cust-dup-pg", Name: "Conflicting PG", Slug: "conflicting-pg"}
	err := st.CustomerCreates().CreateCustomerWithOrgBinding(ctx,
		store.CustomerBindingCreateCommand{
			Customer:  conflict,
			OrgID:     org.ID,
			BindingID: "binding-rollback-pg",
		})
	require.Error(t, err)

	_, err = st.Bindings().GetByOrgAndCustomer(ctx, org.ID, seed.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)

	bindings, listErr := st.Bindings().ListByOrg(ctx, org.ID)
	require.NoError(t, listErr)
	assert.Empty(t, bindings)

	var sourceVersion int64
	require.NoError(t, st.SQLDB().QueryRowContext(ctx,
		`SELECT version FROM authorization_source_version WHERE id = TRUE`,
	).Scan(&sourceVersion))
	assert.Zero(t, sourceVersion)
}

func TestCustomerUpdate_CASConflict_Postgres(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	c := &store.Customer{ID: "cust-cas-pg", Name: "CAS Co PG", Slug: "cas-co-pg"}
	require.NoError(t, createCustomerViaManagement(ctx, st, c))
	require.EqualValues(t, 1, c.Version)

	first := &store.Customer{ID: c.ID, Name: "First Write PG", Slug: "cas-co-pg"}
	require.NoError(t, st.Customers().Update(ctx, first, 1))

	second := &store.Customer{ID: c.ID, Name: "Second Write PG", Slug: "cas-co-pg"}
	err := st.Customers().Update(ctx, second, 1)
	assert.ErrorIs(t, err, store.ErrOptimisticLock)

	got, err := st.Customers().Get(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "First Write PG", got.Name)
	assert.EqualValues(t, 2, got.Version)
}
