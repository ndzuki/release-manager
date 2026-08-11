package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

func TestListReleasesFiltersAndPaginates(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()
	seedInventoryScope(t, st, "customer-inventory", "cluster-inventory")
	syncedAt := time.Date(2026, time.July, 22, 10, 0, 0, 0, time.UTC)

	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: "definition-drifted", Name: "drifted", CustomerID: "customer-inventory", ClusterID: "cluster-inventory",
		Namespace: "other", ReleaseName: "api", Status: store.DefStatusActive,
	}, nil))
	require.NoError(t, st.Values().Create(ctx, &store.ValuesRevision{
		ID: "values-drifted", ReleaseDefinitionID: "definition-drifted", Revision: 1,
		Status: store.ValuesStatusApproved, Values: []byte(`{}`), Digest: "sha256:desired",
	}))

	items := []*store.ReleaseInventory{
		{
			CustomerID: "customer-inventory", ClusterID: "cluster-inventory", Namespace: "apps", ReleaseName: "api",
			Chart: "api", ChartVersion: "1.0.0", Revision: 2, ValuesDigest: "sha256:current",
			InventoryStatus: store.InventoryMissing, LastSyncID: "sync-list", SnapshotVersion: 4,
		},
		{
			ReleaseDefinitionID: "definition-drifted", CustomerID: "customer-inventory", ClusterID: "cluster-inventory",
			Namespace: "other", ReleaseName: "api", Chart: "api", ChartVersion: "1.1.0", Revision: 3,
			ValuesDigest: "sha256:actual", InventoryStatus: store.InventoryActive, LastSyncID: "sync-list", SnapshotVersion: 4,
		},
	}
	for _, item := range items {
		require.NoError(t, st.Inventories().Upsert(ctx, item))
	}
	inserted, err := st.Inventories().CreateSyncLog(ctx, &store.InventorySyncLog{
		SyncID: "sync-list", CustomerID: "customer-inventory", ClusterID: "cluster-inventory",
		IsFullSnapshot: true, AcceptedCount: 2, SnapshotVersion: 4, CreatedAt: syncedAt,
	})
	require.NoError(t, err)
	require.True(t, inserted)

	missing, err := svc.ListReleases(ctx, connect.NewRequest(&orchestratorv1.ListReleasesRequest{
		CustomerId: "customer-inventory", ClusterId: "cluster-inventory",
		StatusFilter: orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_MISSING,
		NameSearch:   "API", PageSize: 1,
	}))
	require.NoError(t, err)
	require.Len(t, missing.Msg.GetReleases(), 1)
	assert.Equal(t, "apps", missing.Msg.GetReleases()[0].GetNamespace())
	assert.Equal(t, orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_MISSING, missing.Msg.GetReleases()[0].GetStatus())
	assert.Equal(t, syncedAt, missing.Msg.GetReleases()[0].GetLastSyncAt().AsTime())

	all, err := svc.ListReleases(ctx, connect.NewRequest(&orchestratorv1.ListReleasesRequest{
		CustomerId: "customer-inventory", ClusterId: "cluster-inventory", NameSearch: "api", PageSize: 1,
	}))
	require.NoError(t, err)
	assert.Equal(t, int32(2), all.Msg.GetTotalCount())
	assert.NotEmpty(t, all.Msg.GetNextCursor())

	next, err := svc.ListReleases(ctx, connect.NewRequest(&orchestratorv1.ListReleasesRequest{
		CustomerId: "customer-inventory", ClusterId: "cluster-inventory", NameSearch: "api", PageSize: 1,
		Cursor: all.Msg.GetNextCursor(),
	}))
	require.NoError(t, err)
	require.Len(t, next.Msg.GetReleases(), 1)
	assert.Equal(t, orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_OUT_OF_SYNC, next.Msg.GetReleases()[0].GetStatus())
}

func TestListReleasesValidatesScopeAndCursor(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()
	seedInventoryScope(t, st, "customer-a", "cluster-a")
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: "customer-b", Name: "Other", Slug: "other"}))

	tests := []struct {
		name       string
		request    *orchestratorv1.ListReleasesRequest
		wantCode   connect.Code
		wantReason string
	}{
		{name: "customer required", request: &orchestratorv1.ListReleasesRequest{ClusterId: "cluster-a"}, wantCode: connect.CodeInvalidArgument, wantReason: "customer_id_required"},
		{name: "cluster required", request: &orchestratorv1.ListReleasesRequest{CustomerId: "customer-a"}, wantCode: connect.CodeInvalidArgument, wantReason: "cluster_id_required"},
		{name: "search bounded", request: &orchestratorv1.ListReleasesRequest{CustomerId: "customer-a", ClusterId: "cluster-a", NameSearch: strings.Repeat("x", 254)}, wantCode: connect.CodeInvalidArgument, wantReason: "name_search_too_long"},
		{name: "customer missing", request: &orchestratorv1.ListReleasesRequest{CustomerId: "missing", ClusterId: "cluster-a"}, wantCode: connect.CodeNotFound, wantReason: "customer_not_found"},
		{name: "cluster missing", request: &orchestratorv1.ListReleasesRequest{CustomerId: "customer-a", ClusterId: "missing"}, wantCode: connect.CodeNotFound, wantReason: "cluster_not_found"},
		{name: "cross customer hidden", request: &orchestratorv1.ListReleasesRequest{CustomerId: "customer-b", ClusterId: "cluster-a"}, wantCode: connect.CodeNotFound, wantReason: "cluster_not_found"},
		{name: "invalid cursor", request: &orchestratorv1.ListReleasesRequest{CustomerId: "customer-a", ClusterId: "cluster-a", Cursor: "invalid"}, wantCode: connect.CodeInvalidArgument, wantReason: "invalid_cursor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.ListReleases(ctx, connect.NewRequest(tt.request))
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Equal(t, tt.wantReason, connectErr.Meta().Get("X-Reason-Code"))
		})
	}
}

func TestListReleasesClampsPageSize(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()
	seedInventoryScope(t, st, "customer-clamp", "cluster-clamp")

	// page_size > 100 不再报错，而是按共享契约 clamp 到 100（REQ-010 输入契约 page_size<=100）。
	for _, size := range []int32{0, 201} {
		resp, err := svc.ListReleases(ctx, connect.NewRequest(&orchestratorv1.ListReleasesRequest{
			CustomerId: "customer-clamp", ClusterId: "cluster-clamp", PageSize: size,
		}))
		require.NoError(t, err)
		assert.Equal(t, int32(0), resp.Msg.GetTotalCount())
		assert.Empty(t, resp.Msg.GetNextCursor())
	}
}

func TestTriggerInventorySyncCreatesOneDurableCommand(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()
	seedInventoryScope(t, st, "customer-sync", "cluster-sync")
	require.NoError(t, st.Operators().Create(ctx, &store.Operator{
		ID: "operator-sync", CustomerID: "customer-sync", ClusterID: "cluster-sync", CertSerial: "sync-cert",
	}))
	require.NoError(t, st.Sessions().Create(ctx, &store.Session{
		ID: "session-sync", OperatorID: "operator-sync", Status: store.SessionOnline,
		StartedAt: time.Now(), LastHeartbeat: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))

	first, err := svc.TriggerInventorySync(ctx, connect.NewRequest(&orchestratorv1.TriggerInventorySyncRequest{
		CustomerId: "customer-sync", ClusterId: "cluster-sync",
	}))
	require.NoError(t, err)
	require.NotEmpty(t, first.Msg.GetSyncRequestId())

	request, err := st.InventorySyncRequests().Get(ctx, first.Msg.GetSyncRequestId())
	require.NoError(t, err)
	assert.Equal(t, store.InventorySyncPending, request.Status)
	outbox, err := st.Outbox().GetByCommandID(ctx, request.CommandID)
	require.NoError(t, err)
	assert.Equal(t, inventorySyncOperationType, outbox.OperationType)
	assert.Contains(t, string(outbox.Payload), first.Msg.GetSyncRequestId())

	_, err = svc.TriggerInventorySync(ctx, connect.NewRequest(&orchestratorv1.TriggerInventorySyncRequest{
		CustomerId: "customer-sync", ClusterId: "cluster-sync",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "sync_in_progress", connectErr.Meta().Get("X-Reason-Code"))
	assert.Equal(t, first.Msg.GetSyncRequestId(), connectErr.Meta().Get("X-Sync-Request-ID"))
}

func TestTriggerInventorySyncRejectsOfflineOperator(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()
	seedInventoryScope(t, st, "customer-offline", "cluster-offline")
	require.NoError(t, st.Operators().Create(ctx, &store.Operator{
		ID: "operator-offline", CustomerID: "customer-offline", ClusterID: "cluster-offline", CertSerial: "offline-cert",
	}))

	_, err := svc.TriggerInventorySync(ctx, connect.NewRequest(&orchestratorv1.TriggerInventorySyncRequest{
		CustomerId: "customer-offline", ClusterId: "cluster-offline",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "operator_offline", connectErr.Meta().Get("X-Reason-Code"))
}

func seedInventoryScope(t *testing.T, st store.Store, customerID, clusterID string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: customerID, Name: customerID, Slug: customerID}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: clusterID, Name: clusterID, CustomerID: customerID}))
}
