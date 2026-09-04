//go:build integration

package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// seedInventoryRow upserts a minimal inventory row for the identity tests.
func seedInventoryRow(t *testing.T, st interface {
	Inventories() store.InventoryStore
}, customerID, clusterID, namespace, releaseName string) {
	t.Helper()
	item := &store.ReleaseInventory{
		ReleaseDefinitionID: "definition-1",
		CustomerID:          customerID,
		ClusterID:           clusterID,
		Namespace:           namespace,
		ReleaseName:         releaseName,
		Chart:               "example-chart",
		ChartVersion:        "1.0.0",
		Revision:            1,
		Status:              "deployed",
		InventoryStatus:     store.InventoryActive,
	}
	require.NoError(t, st.Inventories().Upsert(t.Context(), item))
}

// AC-085-01/04 (postgres seam, migrations/000024): UpdateWorkloadIdentity
// persists the authoritative identity on the unique-key row and is idempotent;
// unknown rows return store.ErrNotFound (fail-closed).
func TestInventoryUpdateWorkloadIdentity(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedInventoryRow(t, st, "customer-1", "cluster-1", "apps", "example")

	identity := store.WorkloadIdentity{Kind: "DEPLOYMENT", Name: "example", Namespace: "apps", UID: "uid-0001"}
	require.NoError(t, st.Inventories().UpdateWorkloadIdentity(ctx, "customer-1", "cluster-1", "apps", "example", identity))

	items, err := st.Inventories().ListByCluster(ctx, "customer-1", "cluster-1")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "DEPLOYMENT", items[0].WorkloadKind)
	assert.Equal(t, "example", items[0].WorkloadName)
	assert.Equal(t, "apps", items[0].WorkloadNamespace)
	assert.Equal(t, "uid-0001", items[0].WorkloadUID)

	// Idempotent: reapplying the same identity succeeds and leaves the row intact.
	require.NoError(t, st.Inventories().UpdateWorkloadIdentity(ctx, "customer-1", "cluster-1", "apps", "example", identity))
	items, err = st.Inventories().ListByCluster(ctx, "customer-1", "cluster-1")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "uid-0001", items[0].WorkloadUID)

	// Unknown row → ErrNotFound, no implicit insert.
	err = st.Inventories().UpdateWorkloadIdentity(ctx, "customer-1", "cluster-1", "apps", "missing", identity)
	require.ErrorIs(t, err, store.ErrNotFound)
	items, err = st.Inventories().ListByCluster(ctx, "customer-1", "cluster-1")
	require.NoError(t, err)
	require.Len(t, items, 1)
}

// REQ-085 additive boundary (postgres): a later inventory sync Upsert whose
// rows carry no identity columns must not clobber a previously reported
// identity (D-110 ②).
func TestInventoryUpsertPreservesWorkloadIdentity(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedInventoryRow(t, st, "customer-1", "cluster-1", "apps", "example")

	identity := store.WorkloadIdentity{Kind: "STATEFUL_SET", Name: "example-sts", Namespace: "apps", UID: "uid-sts"}
	require.NoError(t, st.Inventories().UpdateWorkloadIdentity(ctx, "customer-1", "cluster-1", "apps", "example", identity))

	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		CustomerID:      "customer-1",
		ClusterID:       "cluster-1",
		Namespace:       "apps",
		ReleaseName:     "example",
		Chart:           "example-chart",
		Revision:        2,
		Status:          "deployed",
		InventoryStatus: store.InventoryActive,
	}))

	items, err := st.Inventories().ListByCluster(ctx, "customer-1", "cluster-1")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, 2, items[0].Revision)
	assert.Equal(t, "STATEFUL_SET", items[0].WorkloadKind)
	assert.Equal(t, "example-sts", items[0].WorkloadName)
	assert.Equal(t, "uid-sts", items[0].WorkloadUID)
}
