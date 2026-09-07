package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// seedInventoryItem upserts a minimal inventory row for the identity tests.
func seedInventoryItem(t *testing.T, st interface {
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

// AC-085-01/04 (store seam): UpdateWorkloadIdentity persists the authoritative
// identity on the unique-key row and is idempotent; unknown rows return
// store.ErrNotFound (fail-closed: identity is never inserted implicitly).
func TestInventoryUpdateWorkloadIdentity(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedInventoryItem(t, st, "customer-1", "cluster-1", "apps", "example")

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

// REQ-085 additive boundary: a later inventory sync Upsert (whose rows carry
// no identity columns) must not clobber a previously reported identity — the
// ON CONFLICT update preserves non-empty identity values (D-110 ②).
func TestInventoryUpsertPreservesWorkloadIdentity(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedInventoryItem(t, st, "customer-1", "cluster-1", "apps", "example")

	identity := store.WorkloadIdentity{Kind: "STATEFUL_SET", Name: "example-sts", Namespace: "apps", UID: "uid-sts"}
	require.NoError(t, st.Inventories().UpdateWorkloadIdentity(ctx, "customer-1", "cluster-1", "apps", "example", identity))

	// Sync-style upsert: same key, empty identity columns.
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

// ── REQ-088 (TASK-088): pending workload identity buffer ──

// seedPendingCustomerCluster creates the customer/cluster FK targets required
// by pending_workload_identity (ON DELETE CASCADE).
func seedPendingCustomerCluster(t *testing.T, st interface {
	Customers() store.CustomerStore
	Clusters() store.ClusterStore
}) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: "customer-1", Name: "test-customer", Slug: "test-pending", Status: store.CustomerActive, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: "cluster-1", Name: "test-cluster", CustomerID: "customer-1", Status: store.ClusterActive, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
}

// pendingIdentityFor builds the REQ-088 pending identity fixture. The row id
// derives from the release name so independent release keys never collide on
// the TEXT PRIMARY KEY.
func pendingIdentityFor(t *testing.T, namespace, releaseName, uid string) *store.PendingWorkloadIdentity {
	t.Helper()
	return &store.PendingWorkloadIdentity{
		ID: "pending-" + releaseName, CustomerID: "customer-1", ClusterID: "cluster-1",
		Namespace: namespace, ReleaseName: releaseName,
		WorkloadKind: "DEPLOYMENT", WorkloadName: "example", WorkloadNamespace: "apps", WorkloadUID: uid,
		CreatedAt: time.Now().UTC(),
	}
}

// AC-088-05/08 (store seam, D2=D6=A): Upsert persists the buffered report and
// is idempotent by the release key — a second upsert for the same key replaces
// the four-tuple and refreshes created_at without duplicating rows.
func TestPendingWorkloadIdentityUpsertIdempotentByReleaseKey(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedPendingCustomerCluster(t, st)

	first := pendingIdentityFor(t, "apps", "example", "uid-0001")
	require.NoError(t, st.PendingWorkloadIdentities().Upsert(ctx, first))

	older := time.Now().UTC().Add(-time.Minute)
	second := pendingIdentityFor(t, "apps", "example", "uid-0002")
	second.CreatedAt = older
	require.NoError(t, st.PendingWorkloadIdentities().Upsert(ctx, second))

	got, err := st.PendingWorkloadIdentities().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, "uid-0002", got.WorkloadUID, "same release key must replace the buffered four-tuple")
	// created_at is refreshed to the re-report arrival time (TTL restart),
	// not the caller-supplied stale value.
	assert.False(t, got.CreatedAt.Equal(older), "created_at must be refreshed on conflict")
	assert.WithinDuration(t, time.Now(), got.CreatedAt, time.Minute, "conflict refresh must restart the TTL from now")

	rows, err := st.PendingWorkloadIdentities().ListByCluster(ctx, "customer-1", "cluster-1")
	require.NoError(t, err)
	require.Len(t, rows, 1, "same-key upserts must not duplicate rows")
}

// AC-088-03 (store seam): a missing release key returns ErrNotFound; delete is
// idempotent (no error for an absent key).
func TestPendingWorkloadIdentityGetAndDeleteByReleaseKey(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedPendingCustomerCluster(t, st)

	_, err := st.PendingWorkloadIdentities().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "missing")
	require.ErrorIs(t, err, store.ErrNotFound)

	require.NoError(t, st.PendingWorkloadIdentities().Upsert(ctx, pendingIdentityFor(t, "apps", "example", "uid-0001")))
	require.NoError(t, st.PendingWorkloadIdentities().DeleteByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "example"))
	_, err = st.PendingWorkloadIdentities().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "example")
	require.ErrorIs(t, err, store.ErrNotFound)

	// Deleting an already-deleted key is a no-op success.
	require.NoError(t, st.PendingWorkloadIdentities().DeleteByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "example"))
}

// AC-088-06 (store seam, D3=A): PurgeExpired removes only rows whose
// created_at precedes the cutoff — a fresh pending row survives.
func TestPendingWorkloadIdentityPurgeExpired(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedPendingCustomerCluster(t, st)

	fresh := pendingIdentityFor(t, "apps", "fresh", "uid-fresh")
	fresh.CreatedAt = time.Now().UTC()
	require.NoError(t, st.PendingWorkloadIdentities().Upsert(ctx, fresh))

	stale := pendingIdentityFor(t, "apps", "stale", "uid-stale")
	stale.CreatedAt = time.Now().UTC().Add(-30 * time.Minute)
	require.NoError(t, st.PendingWorkloadIdentities().Upsert(ctx, stale))

	cutoff := time.Now().UTC().Add(-10 * time.Minute)
	purged, err := st.PendingWorkloadIdentities().PurgeExpired(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), purged)

	_, err = st.PendingWorkloadIdentities().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "stale")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.PendingWorkloadIdentities().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "fresh")
	require.NoError(t, err)
}

// AC-088-01 (inventory seam): GetByReleaseKey resolves the row by the exact
// inventory unique key (the replay precondition).
func TestInventoryGetByReleaseKey(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedInventoryItem(t, st, "customer-1", "cluster-1", "apps", "example")

	got, err := st.Inventories().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, "example", got.ReleaseName)
	assert.Equal(t, "definition-1", got.ReleaseDefinitionID)

	_, err = st.Inventories().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "missing")
	require.ErrorIs(t, err, store.ErrNotFound)
}
