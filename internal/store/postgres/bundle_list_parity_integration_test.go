//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-199/TASK-215: the list filter cannot reuse store.ReachableBundlePredicate verbatim
// (it is a WHERE fragment, not a SELECT), so PostgreSQL carries its own copy of the same
// relationship. An independent review flagged that as a drift surface; this test binds the
// two together: whatever the page shows for an organization must be exactly what the
// reachability predicate says that organization may read, row for row.
func TestBundleListMatchesReachability(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Organization "org-a" owns definition-a (through an owner id, not a binding, so the
	// two predicate branches are both exercised); org-b is a stranger.
	orgA, orgB := "org-parity-a", "org-parity-b"
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: orgA, Name: orgA}))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: orgB, Name: orgB}))
	customer := "cust-parity"
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{
		ID: customer, Name: customer, Slug: customer, Status: store.CustomerActive,
	}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{
		ID: "cls-parity", CustomerID: customer, Name: "cls-parity", Status: store.ClusterActive,
	}))
	defA := &store.ReleaseDefinition{
		ID: "def-parity-a", Name: "app", CustomerID: customer, ClusterID: "cls-parity",
		Namespace: "default", ReleaseName: "app", ChartName: "app", Status: store.DefStatusActive,
		CreatedAt: now, OwnerOrganizationID: &orgA,
	}
	require.NoError(t, st.Definitions().Create(ctx, defA, nil))

	seed := func(id string) *store.ReleaseBundle {
		t.Helper()
		bundle := &store.ReleaseBundle{
			ID: id, Name: id, DigestAlg: "sha256", DigestValue: uuid.NewString(),
			Status: store.BundleValidated, ChartRef: "app", CreatedAt: now,
		}
		require.NoError(t, st.Bundles().Create(ctx, bundle))
		return bundle
	}
	owned := seed("bundle-parity-owned")
	byOperation := seed("bundle-parity-operation")
	unclaimed := seed("bundle-parity-unclaimed")
	_, err := st.Definitions().SetCurrentBundle(ctx, defA.ID, owned.ID)
	require.NoError(t, err)
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: uuid.NewString(), ReleaseDefinitionID: defA.ID, BundleID: byOperation.ID,
		Status: store.StatusPending, CreatedAt: now,
	}))

	page, err := st.Bundles().List(ctx, store.BundleListFilter{
		OrganizationID: orgA, Statuses: []store.BundleStatus{store.BundleValidated}, PageSize: 50,
	})
	require.NoError(t, err)

	listed := map[string]bool{}
	for _, bundle := range page.Bundles {
		listed[bundle.ID] = true
		reachable, reachErr := st.Bundles().ReachableFromOrganization(ctx, bundle.ID, orgA)
		require.NoError(t, reachErr)
		assert.True(t, reachable, "the list showed %s but reachability denies it", bundle.ID)
	}
	for _, bundle := range []*store.ReleaseBundle{owned, byOperation} {
		assert.True(t, listed[bundle.ID], "reachability allows %s but the list hid it", bundle.ID)
	}
	assert.False(t, listed[unclaimed.ID], "an unclaimed bundle must not appear")

	// The stranger sees nothing, and the same rows stay unreachable for it.
	strangerPage, err := st.Bundles().List(ctx, store.BundleListFilter{
		OrganizationID: orgB, Statuses: []store.BundleStatus{store.BundleValidated}, PageSize: 50,
	})
	require.NoError(t, err)
	assert.Empty(t, strangerPage.Bundles, "a foreign organization must see no rows")
	for _, bundle := range []*store.ReleaseBundle{owned, byOperation} {
		reachable, reachErr := st.Bundles().ReachableFromOrganization(ctx, bundle.ID, orgB)
		require.NoError(t, reachErr)
		assert.False(t, reachable)
		claimed, claimErr := st.Bundles().BundleClaimedOutsideOrganization(ctx, bundle.ID, orgB)
		require.NoError(t, claimErr)
		assert.True(t, claimed, "the write path must see the foreign claim")
	}
}
