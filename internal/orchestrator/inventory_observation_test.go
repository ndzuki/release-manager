package orchestrator

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestListReleaseInventoryObservesBoundCustomersDeterministically(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := t.Context()

	seedInventoryObservationOrg(t, st)
	seedInventoryObservationScope(t, st, "cust-b", "cluster-z", "Customer B", "Cluster Z")
	seedInventoryObservationScope(t, st, "cust-a", "cluster-a", "Customer A", "Cluster A")
	seedInventoryObservationScope(t, st, "cust-hidden", "cluster-hidden", "Hidden Customer", "Hidden Cluster")
	seedInventoryObservationBinding(t, st, "binding-b", "cust-b", store.BindingActive)
	seedInventoryObservationBinding(t, st, "binding-a", "cust-a", store.BindingActive)
	seedInventoryObservationBinding(t, st, "binding-hidden", "cust-hidden", store.BindingRevoked)

	seedInventoryObservationDefinition(t, st, "def-b", "release-b", "cust-b", "cluster-z")
	seedInventoryObservationDefinition(t, st, "def-a", "release-a", "cust-a", "cluster-a")
	seedInventoryObservationDefinition(t, st, "def-hidden", "release-hidden", "cust-hidden", "cluster-hidden")

	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		ReleaseDefinitionID:    "def-b",
		CustomerID:             "cust-b",
		ClusterID:              "cluster-z",
		Namespace:              "apps",
		ReleaseName:            "release-b",
		Revision:               7,
		Status:                 "deployed",
		InventoryStatus:        store.InventoryOutOfSync,
		ValuesDigest:           "sensitive-values-digest",
		ObservedBundleDigest:   "sensitive-bundle-digest",
		ObservedChartDigest:    "sensitive-chart-digest",
		ObservedManifestDigest: "sensitive-manifest-digest",
		WorkloadUID:            "sensitive-workload-uid",
		LastSyncID:             "sensitive-sync-id",
	}))
	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		ReleaseDefinitionID: "def-a",
		CustomerID:          "cust-a",
		ClusterID:           "cluster-a",
		Namespace:           "default",
		ReleaseName:         "release-a",
		Revision:            3,
		Status:              "failed",
		InventoryStatus:     store.InventoryMissing,
	}))
	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		ReleaseDefinitionID: "def-hidden",
		CustomerID:          "cust-hidden",
		ClusterID:           "cluster-hidden",
		Namespace:           "default",
		ReleaseName:         "release-hidden",
		Revision:            99,
		InventoryStatus:     store.InventoryActive,
	}))

	createdAt := time.Date(2026, time.January, 3, 4, 5, 6, 0, time.UTC)
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID:                  "op-cancelling",
		OperationType:       store.OperationUpgrade,
		Status:              store.StatusCancelling,
		ReleaseDefinitionID: "def-b",
		IdempotencyKey:      "idem-cancelling",
		RequestHash:         "hash-cancelling",
		Actor:               store.ActorContext{UserID: "actor-user", Organization: "org-001"},
		CreatedAt:           createdAt,
		UpdatedAt:           createdAt,
	}))
	terminalAt := createdAt.Add(time.Minute)
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID:                  "op-terminal",
		OperationType:       store.OperationInstall,
		Status:              store.StatusSucceeded,
		ReleaseDefinitionID: "def-a",
		IdempotencyKey:      "idem-terminal",
		RequestHash:         "hash-terminal",
		Actor:               store.ActorContext{UserID: "terminal-user", Organization: "org-001"},
		CreatedAt:           createdAt,
		UpdatedAt:           terminalAt,
		TerminalAt:          &terminalAt,
	}))

	resp, err := svc.ListReleaseInventory(adminCtx(), connect.NewRequest(&orchestratorv1.ListReleaseInventoryRequest{}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetRows(), 2, "only active org bindings are visible")

	rows := resp.Msg.GetRows()
	assert.Equal(t, []string{"cust-a", "cust-b"}, []string{rows[0].GetCustomerId(), rows[1].GetCustomerId()})
	assert.Equal(t, "Customer A", rows[0].GetCustomerName())
	assert.Equal(t, "Cluster A", rows[0].GetClusterName())
	assert.Equal(t, "release-a", rows[0].GetReleaseName())
	assert.Equal(t, "def-a", rows[0].GetReleaseDefinitionId())
	assert.Equal(t, int32(3), rows[0].GetRevision())
	assert.Equal(t, orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_MISSING, rows[0].GetStatus())
	assert.Nil(t, rows[0].GetActiveOperation(), "terminal operations are omitted")

	assert.Equal(t, "Customer B", rows[1].GetCustomerName())
	assert.Equal(t, "Cluster Z", rows[1].GetClusterName())
	assert.Equal(t, "release-b", rows[1].GetReleaseName())
	assert.Equal(t, "def-b", rows[1].GetReleaseDefinitionId())
	assert.Equal(t, int32(7), rows[1].GetRevision())
	assert.Equal(t, orchestratorv1.ReleaseInventoryStatus_RELEASE_INVENTORY_STATUS_OUT_OF_SYNC, rows[1].GetStatus())
	active := rows[1].GetActiveOperation()
	require.NotNil(t, active)
	assert.Equal(t, "op-cancelling", active.GetOperationId())
	assert.Equal(t, string(store.OperationUpgrade), active.GetOperationType())
	assert.Equal(t, orchestratorv1.OperationStatus_OPERATION_STATUS_CANCELLING, active.GetState())
	assert.Equal(t, "actor-user", active.GetActor())

	encoded, err := protojson.Marshal(resp.Msg)
	require.NoError(t, err)
	responseJSON := string(encoded)
	for _, sensitive := range []string{
		"values_digest", "observed_bundle_digest", "observed_chart_digest", "observed_manifest_digest",
		"workload_uid", "last_sync_id", "sensitive-values-digest", "sensitive-workload-uid", "sensitive-sync-id",
	} {
		assert.NotContains(t, responseJSON, sensitive)
	}
}

func TestListReleaseInventoryRequiresAuthenticationAndBinding(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := t.Context()
	seedInventoryObservationScope(t, st, "cust-unbound", "cluster-unbound", "Unbound Customer", "Unbound Cluster")
	seedInventoryObservationDefinition(t, st, "def-unbound", "release-unbound", "cust-unbound", "cluster-unbound")
	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		ReleaseDefinitionID: "def-unbound",
		CustomerID:          "cust-unbound",
		ClusterID:           "cluster-unbound",
		Namespace:           "default",
		ReleaseName:         "release-unbound",
		InventoryStatus:     store.InventoryActive,
	}))

	_, err := svc.ListReleaseInventory(t.Context(), connect.NewRequest(&orchestratorv1.ListReleaseInventoryRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	resp, err := svc.ListReleaseInventory(adminCtx(), connect.NewRequest(&orchestratorv1.ListReleaseInventoryRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetRows(), "an actor without active customer bindings sees no inventory")
}

func seedInventoryObservationOrg(t *testing.T, st store.Store) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-001", Name: "Observation Org"}))
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: "user-001", Username: "user-001", Status: store.UserActive}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-001", UserID: "user-001", Role: store.RoleReleaseAdmin,
	}))
}

func seedInventoryObservationBinding(t *testing.T, st store.Store, id, customerID string, status store.BindingStatus) {
	t.Helper()
	require.NoError(t, st.Bindings().Create(t.Context(), &store.OrgCustomerBinding{
		ID: id, OrgID: "org-001", CustomerID: customerID, Status: status,
	}))
}

func seedInventoryObservationScope(t *testing.T, st store.Store, customerID, clusterID, customerName, clusterName string) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: customerID, Name: customerName, Slug: customerID}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: clusterID, Name: clusterName, CustomerID: customerID}))
}

func seedInventoryObservationDefinition(t *testing.T, st store.Store, id, name, customerID, clusterID string) {
	t.Helper()
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: id, Name: name, CustomerID: customerID, ClusterID: clusterID,
		Namespace: "default", ReleaseName: name, ChartName: "example", Status: store.DefStatusActive,
	}, nil))
}
