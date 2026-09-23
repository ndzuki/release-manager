//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
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

func inventoryKey(item *store.ReleaseInventory) string {
	return item.CustomerID + "/" + item.ClusterID + "/" + item.Namespace + "/" + item.ReleaseName
}

// TestInventoryListAllReturnsEveryScopeInStableOrder covers the observable
// store seam only: callers receive every customer/cluster row in key order.
func TestInventoryListAllReturnsEveryScopeInStableOrder(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	for _, item := range []struct {
		customerID string
		clusterID  string
		namespace  string
		release    string
	}{
		{customerID: "customer-b", clusterID: "cluster-z", namespace: "ops", release: "zulu"},
		{customerID: "customer-a", clusterID: "cluster-z", namespace: "apps", release: "beta"},
		{customerID: "customer-a", clusterID: "cluster-a", namespace: "ops", release: "alpha"},
		{customerID: "customer-a", clusterID: "cluster-a", namespace: "apps", release: "zulu"},
		{customerID: "customer-a", clusterID: "cluster-a", namespace: "apps", release: "alpha"},
	} {
		seedInventoryRow(t, st, item.customerID, item.clusterID, item.namespace, item.release)
	}

	want := []string{
		"customer-a/cluster-a/apps/alpha",
		"customer-a/cluster-a/apps/zulu",
		"customer-a/cluster-a/ops/alpha",
		"customer-a/cluster-z/apps/beta",
		"customer-b/cluster-z/ops/zulu",
	}

	list, err := st.Inventories().ListAll(ctx)
	require.NoError(t, err)
	require.Len(t, list, len(want))
	got := make([]string, 0, len(list))
	for _, item := range list {
		got = append(got, inventoryKey(item))
	}
	assert.Equal(t, want, got)

	list, err = st.Inventories().ListAll(ctx)
	require.NoError(t, err)
	got = got[:0]
	for _, item := range list {
		got = append(got, inventoryKey(item))
	}
	assert.Equal(t, want, got)
}

func TestInventoryListAllReturnsEmptyList(t *testing.T) {
	st := setupStore(t)

	list, err := st.Inventories().ListAll(t.Context())
	require.NoError(t, err)
	assert.Empty(t, list)
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

// ── TASK-168 (REQ-058 C1/R1): observed workload field projection (migration 000030) ──

// TestInventoryUpdateWorkloadObservation (postgres seam, migration 000030): the
// observed projection round-trips through the unique-key row, a later
// observation replaces the earlier one, a real zero replica count survives as
// zero rather than collapsing into "not observed", and an unknown row returns
// store.ErrNotFound.
func TestInventoryUpdateWorkloadObservation(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedInventoryRow(t, st, "customer-1", "cluster-1", "apps", "example")

	replicas := int32(3)
	observedAt := time.Now().UTC().Truncate(time.Second)
	observation := store.WorkloadObservation{
		Containers: []string{"api", "sidecar"},
		ImageRefs: map[string]string{
			"api":     "registry.example.com/api:1.2.3",
			"sidecar": "registry.example.com/sidecar:0.4.0",
		},
		Replicas:   &replicas,
		ObservedAt: observedAt,
	}
	require.NoError(t, st.Inventories().UpdateWorkloadObservation(ctx, "customer-1", "cluster-1", "apps", "example", observation))

	got, err := st.Inventories().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, []string{"api", "sidecar"}, got.ObservedContainers)
	assert.Equal(t, observation.ImageRefs, got.ObservedImageRefs)
	require.NotNil(t, got.ObservedReplicas)
	assert.Equal(t, int32(3), *got.ObservedReplicas)
	assert.True(t, got.ObservedAt.Equal(observedAt), "observed_at must round-trip: got %s want %s", got.ObservedAt, observedAt)

	// Last write wins, and a real zero replica count is preserved as zero.
	zero := int32(0)
	require.NoError(t, st.Inventories().UpdateWorkloadObservation(ctx, "customer-1", "cluster-1", "apps", "example", store.WorkloadObservation{
		Containers: []string{"api"},
		ImageRefs:  map[string]string{"api": "registry.example.com/api:1.2.4"},
		Replicas:   &zero,
		ObservedAt: observedAt.Add(time.Minute),
	}))
	got, err = st.Inventories().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, []string{"api"}, got.ObservedContainers)
	assert.Equal(t, map[string]string{"api": "registry.example.com/api:1.2.4"}, got.ObservedImageRefs)
	require.NotNil(t, got.ObservedReplicas)
	assert.Equal(t, int32(0), *got.ObservedReplicas, "a real zero must not collapse into not-observed")

	// Unknown row → ErrNotFound, no implicit insert.
	err = st.Inventories().UpdateWorkloadObservation(ctx, "customer-1", "cluster-1", "apps", "missing", observation)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.Inventories().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "missing")
	require.ErrorIs(t, err, store.ErrNotFound)
}

// TestInventoryObservedWorkloadDefaultsToNotObserved pins the fail-closed
// representation on the postgres engine too: NULL observed_at/observed_replicas
// and the defaulted JSON columns decode to the "not observed" zero values.
func TestInventoryObservedWorkloadDefaultsToNotObserved(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedInventoryRow(t, st, "customer-1", "cluster-1", "apps", "example")

	got, err := st.Inventories().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "example")
	require.NoError(t, err)
	assert.Nil(t, got.ObservedContainers)
	assert.Nil(t, got.ObservedImageRefs)
	assert.Nil(t, got.ObservedReplicas)
	assert.True(t, got.ObservedAt.IsZero())
}

// TestInventoryUpsertPreservesWorkloadObservation (postgres, migration 000030):
// an inventory sync Upsert must not clobber a previously reported observation,
// while an Upsert that carries one writes it on the INSERT path.
func TestInventoryUpsertPreservesWorkloadObservation(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedInventoryRow(t, st, "customer-1", "cluster-1", "apps", "example")

	replicas := int32(4)
	observedAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, st.Inventories().UpdateWorkloadObservation(ctx, "customer-1", "cluster-1", "apps", "example", store.WorkloadObservation{
		Containers: []string{"api"},
		ImageRefs:  map[string]string{"api": "registry.example.com/api:2.0.0"},
		Replicas:   &replicas,
		ObservedAt: observedAt,
	}))

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

	got, err := st.Inventories().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, 2, got.Revision)
	assert.Equal(t, []string{"api"}, got.ObservedContainers, "sync upsert must not clobber the observation")
	assert.Equal(t, map[string]string{"api": "registry.example.com/api:2.0.0"}, got.ObservedImageRefs)
	require.NotNil(t, got.ObservedReplicas)
	assert.Equal(t, int32(4), *got.ObservedReplicas)
	assert.True(t, got.ObservedAt.Equal(observedAt))

	inserted := int32(1)
	insertedAt := observedAt.Add(2 * time.Minute)
	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		CustomerID:         "customer-1",
		ClusterID:          "cluster-1",
		Namespace:          "apps",
		ReleaseName:        "fresh",
		Chart:              "example-chart",
		Revision:           1,
		Status:             "deployed",
		InventoryStatus:    store.InventoryActive,
		ObservedContainers: []string{"fresh-api"},
		ObservedImageRefs:  map[string]string{"fresh-api": "registry.example.com/fresh:1.0.0"},
		ObservedReplicas:   &inserted,
		ObservedAt:         insertedAt,
	}))
	got, err = st.Inventories().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "fresh")
	require.NoError(t, err)
	assert.Equal(t, []string{"fresh-api"}, got.ObservedContainers)
	assert.Equal(t, map[string]string{"fresh-api": "registry.example.com/fresh:1.0.0"}, got.ObservedImageRefs)
	require.NotNil(t, got.ObservedReplicas)
	assert.Equal(t, int32(1), *got.ObservedReplicas)
	assert.True(t, got.ObservedAt.Equal(insertedAt))
}

// ── REQ-088 (TASK-088): pending workload identity buffer ──

// seedPendingCustomerClusterPg creates the customer/cluster FK targets that
// pending_workload_identity references (ON DELETE CASCADE).
func seedPendingCustomerClusterPg(t *testing.T, st interface {
	Customers() store.CustomerStore
	Clusters() store.ClusterStore
}, customerID, clusterID string) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{
		ID: customerID, Name: "pending-test-customer", Slug: "pending-" + uuid.NewString()[:8],
		Status: store.CustomerActive, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{
		ID: clusterID, Name: "pending-test-cluster", CustomerID: customerID,
		Status: store.ClusterActive, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))
}

func pendingIdentityPg(customerID, clusterID, releaseName, uid string, createdAt time.Time) *store.PendingWorkloadIdentity {
	return &store.PendingWorkloadIdentity{
		ID: "pending-" + uuid.NewString(), CustomerID: customerID, ClusterID: clusterID,
		Namespace: "apps", ReleaseName: releaseName,
		WorkloadKind: "DEPLOYMENT", WorkloadName: "example", WorkloadNamespace: "apps", WorkloadUID: uid,
		CreatedAt: createdAt,
	}
}

// AC-088-05/08 (postgres seam, migration 000025): Upsert persists the
// buffered report and is idempotent by the release key — a second upsert
// replaces the four-tuple without duplicating rows.
func TestPendingWorkloadIdentityUpsertIdempotentByReleaseKey(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	const customerID, clusterID = "customer-pending", "cluster-pending"
	seedPendingCustomerClusterPg(t, st, customerID, clusterID)

	first := pendingIdentityPg(customerID, clusterID, "example", "uid-0001", time.Now().UTC())
	require.NoError(t, st.PendingWorkloadIdentities().Upsert(ctx, first))

	second := pendingIdentityPg(customerID, clusterID, "example", "uid-0002", time.Now().UTC().Add(-time.Minute))
	require.NoError(t, st.PendingWorkloadIdentities().Upsert(ctx, second))

	got, err := st.PendingWorkloadIdentities().GetByReleaseKey(ctx, customerID, clusterID, "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, "uid-0002", got.WorkloadUID, "same release key must replace the buffered four-tuple")
	// created_at is refreshed to the re-report arrival time (TTL restart).
	assert.WithinDuration(t, time.Now(), got.CreatedAt, time.Minute, "conflict refresh must restart the TTL from now")

	rows, err := st.PendingWorkloadIdentities().ListByCluster(ctx, customerID, clusterID)
	require.NoError(t, err)
	require.Len(t, rows, 1, "same-key upserts must not duplicate rows")
}

// AC-088-03 (postgres seam): a missing release key returns ErrNotFound and
// delete is idempotent.
func TestPendingWorkloadIdentityGetAndDeleteByReleaseKey(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	const customerID, clusterID = "customer-pending-del", "cluster-pending-del"
	seedPendingCustomerClusterPg(t, st, customerID, clusterID)

	_, err := st.PendingWorkloadIdentities().GetByReleaseKey(ctx, customerID, clusterID, "apps", "missing")
	require.ErrorIs(t, err, store.ErrNotFound)

	require.NoError(t, st.PendingWorkloadIdentities().Upsert(ctx, pendingIdentityPg(customerID, clusterID, "example", "uid-0001", time.Now().UTC())))
	require.NoError(t, st.PendingWorkloadIdentities().DeleteByReleaseKey(ctx, customerID, clusterID, "apps", "example"))
	_, err = st.PendingWorkloadIdentities().GetByReleaseKey(ctx, customerID, clusterID, "apps", "example")
	require.ErrorIs(t, err, store.ErrNotFound)

	require.NoError(t, st.PendingWorkloadIdentities().DeleteByReleaseKey(ctx, customerID, clusterID, "apps", "example"))
}

// AC-088-06 (postgres seam, D3=A): PurgeExpired removes only rows whose
// created_at precedes the cutoff.
func TestPendingWorkloadIdentityPurgeExpired(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	const customerID, clusterID = "customer-pending-purge", "cluster-pending-purge"
	seedPendingCustomerClusterPg(t, st, customerID, clusterID)

	require.NoError(t, st.PendingWorkloadIdentities().Upsert(ctx, pendingIdentityPg(customerID, clusterID, "fresh", "uid-fresh", time.Now().UTC())))
	require.NoError(t, st.PendingWorkloadIdentities().Upsert(ctx, pendingIdentityPg(customerID, clusterID, "stale", "uid-stale", time.Now().UTC().Add(-30*time.Minute))))

	purged, err := st.PendingWorkloadIdentities().PurgeExpired(ctx, time.Now().UTC().Add(-10*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(1), purged)

	_, err = st.PendingWorkloadIdentities().GetByReleaseKey(ctx, customerID, clusterID, "apps", "stale")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.PendingWorkloadIdentities().GetByReleaseKey(ctx, customerID, clusterID, "apps", "fresh")
	require.NoError(t, err)
}

// AC-088-01 (inventory seam): GetByReleaseKey resolves the row by the exact
// inventory unique key (the replay precondition).
func TestInventoryGetByReleaseKey(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	seedInventoryRow(t, st, "customer-1", "cluster-1", "apps", "example")

	got, err := st.Inventories().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "example")
	require.NoError(t, err)
	assert.Equal(t, "example", got.ReleaseName)
	assert.Equal(t, "definition-1", got.ReleaseDefinitionID)

	_, err = st.Inventories().GetByReleaseKey(ctx, "customer-1", "cluster-1", "apps", "missing")
	require.ErrorIs(t, err, store.ErrNotFound)
}

// TestInventoryQueryPagesWithoutDuplicatingTheOverflowRow pins the page-size
// contract on the PostgreSQL engine: a page never returns more than PageSize
// rows, and the overflow row that proves a next page exists must not reappear
// as the first row of that page. Query selects LIMIT pageSize+1, so the trim is
// the only thing keeping the two engines in agreement; the SQLite engine covers
// the same contract in TestInventoryQueryUsesDefaultPageSizeAndSyncLogTimestamp.
func TestInventoryQueryPagesWithoutDuplicatingTheOverflowRow(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	for _, release := range []string{"alpha", "beta", "gamma"} {
		seedInventoryRow(t, st, "customer-1", "cluster-1", "apps", release)
	}

	first, err := st.Inventories().Query(ctx, store.InventoryQuery{
		CustomerID: "customer-1",
		ClusterID:  "cluster-1",
		PageSize:   2,
	})
	require.NoError(t, err)
	require.Len(t, first.Items, 2, "the LIMIT pageSize+1 overflow row must be trimmed")
	require.NotEmpty(t, first.NextCursor)
	assert.Equal(t, 3, first.TotalCount)

	second, err := st.Inventories().Query(ctx, store.InventoryQuery{
		CustomerID: "customer-1",
		ClusterID:  "cluster-1",
		PageSize:   2,
		Cursor:     first.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	assert.Empty(t, second.NextCursor)

	seen := map[string]int{}
	for _, item := range append(append([]*store.ReleaseInventory{}, first.Items...), second.Items...) {
		seen[item.ReleaseName]++
	}
	assert.Equal(t, map[string]int{"alpha": 1, "beta": 1, "gamma": 1}, seen,
		"each release must appear on exactly one page")
}
