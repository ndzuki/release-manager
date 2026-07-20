package orchestrator

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

func seedCustomer(t *testing.T, st store.Store, id, name string) {
	t.Helper()
	c := &store.Customer{ID: id, Name: name, Slug: id}
	require.NoError(t, st.Customers().Create(context.Background(), c))
}

func seedCluster(t *testing.T, st store.Store, id, customerID string) {
	t.Helper()
	c := &store.Cluster{ID: id, Name: id, CustomerID: customerID}
	require.NoError(t, st.Clusters().Create(context.Background(), c))
}

// ── CreateReleaseDefinition ───────────────────────────────

func TestCreateReleaseDefinition_Success(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-1", "acme")
	seedCluster(t, st, "cls-1", "cust-1")

	resp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId:  "cust-1",
			ClusterId:   "cls-1",
			Namespace:   "default",
			ReleaseName: "my-release",
			ChartName:   "nginx",
			Enabled:     true,
		},
	))
	require.NoError(t, err)
	def := resp.Msg.Definition
	assert.NotEmpty(t, def.Id)
	assert.Equal(t, "cust-1", def.CustomerId)
	assert.Equal(t, "my-release", def.ReleaseName)
	assert.Equal(t, "active", def.Status)
	assert.Equal(t, int64(1), def.Version)

	// AC-040-01: Verify no Helm release interaction — only store-level operations.
	got, err := st.Definitions().Get(context.Background(), def.Id)
	require.NoError(t, err)
	assert.Equal(t, store.DefStatusActive, got.Status)
	assert.NotEmpty(t, got.ID)

	// Verify domain event persisted.
	events, err := st.DefinitionEvents().List(context.Background(), def.Id)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "definition_created", events[0].EventType)
}

func TestCreateReleaseDefinition_DraftWhenDisabled(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-d", "draft-cust")
	seedCluster(t, st, "cls-d", "cust-d")

	resp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId:  "cust-d",
			ClusterId:   "cls-d",
			Namespace:   "staging",
			ReleaseName: "draft-release",
			ChartName:   "app",
			Enabled:     false,
		},
	))
	require.NoError(t, err)
	assert.Equal(t, "draft", resp.Msg.Definition.Status)
}

func TestCreateReleaseDefinition_DuplicateKey(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-2", "beta")
	seedCluster(t, st, "cls-2", "cust-2")

	req := &orchestratorv1.CreateReleaseDefinitionRequest{
		CustomerId:  "cust-2",
		ClusterId:   "cls-2",
		Namespace:   "prod",
		ReleaseName: "unique-rel",
		ChartName:   "nginx",
		Enabled:     true,
	}

	resp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(req))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.Definition.Id)

	// AC-040-02: Second call with same unique key → conflict.
	_, err = svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(req))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "already exists")
}

func TestCreateReleaseDefinition_CustomerNotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId:  "nonexistent",
			ClusterId:   "cls-1",
			Namespace:   "default",
			ReleaseName: "test",
			ChartName:   "nginx",
		},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCreateReleaseDefinition_ClusterNotBelong(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-3", "gamma")
	seedCustomer(t, st, "cust-4", "delta")
	seedCluster(t, st, "cls-4", "cust-4")

	_, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId:  "cust-3",
			ClusterId:   "cls-4",
			Namespace:   "default",
			ReleaseName: "test",
			ChartName:   "nginx",
		},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "does not belong")
}

func TestCreateReleaseDefinition_ClusterDisabled(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-5", "epsilon")
	cls := &store.Cluster{ID: "cls-disabled", Name: "off", CustomerID: "cust-5", Status: store.ClusterDisabled}
	require.NoError(t, st.Clusters().Create(context.Background(), cls))

	_, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId:  "cust-5",
			ClusterId:   "cls-disabled",
			Namespace:   "default",
			ReleaseName: "test",
			ChartName:   "nginx",
		},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// ── GetReleaseDefinition ───────────────────────────────

func TestGetReleaseDefinition_Success(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-g", "zeta")
	seedCluster(t, st, "cls-g", "cust-g")

	createResp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId:  "cust-g",
			ClusterId:   "cls-g",
			Namespace:   "ns",
			ReleaseName: "get-test",
			ChartName:   "app",
			Enabled:     true,
		},
	))
	require.NoError(t, err)

	getResp, err := svc.GetReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.GetReleaseDefinitionRequest{DefinitionId: createResp.Msg.Definition.Id},
	))
	require.NoError(t, err)
	assert.Equal(t, createResp.Msg.Definition.Id, getResp.Msg.Definition.Id)
}

func TestGetReleaseDefinition_NotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.GetReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.GetReleaseDefinitionRequest{DefinitionId: "no-such-def"},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// ── ListReleaseDefinitions ───────────────────────────────

func TestListReleaseDefinitions_ByCustomer(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-list-1", "list-a")
	seedCustomer(t, st, "cust-list-2", "list-b")
	seedCluster(t, st, "cls-list-1", "cust-list-1")
	seedCluster(t, st, "cls-list-2", "cust-list-2")

	_, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId: "cust-list-1", ClusterId: "cls-list-1", Namespace: "ns", ReleaseName: "rel-a", Enabled: true,
		},
	))
	require.NoError(t, err)
	_, err = svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId: "cust-list-2", ClusterId: "cls-list-2", Namespace: "ns", ReleaseName: "rel-b", Enabled: true,
		},
	))
	require.NoError(t, err)

	resp, err := svc.ListReleaseDefinitions(context.Background(), connect.NewRequest(
		&orchestratorv1.ListReleaseDefinitionsRequest{CustomerId: "cust-list-1"},
	))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.Definitions, 1)
	assert.Equal(t, "cust-list-1", resp.Msg.Definitions[0].CustomerId)
}

// ── UpdateReleaseDefinition ───────────────────────────────

func TestUpdateReleaseDefinition_Success(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-upd", "upd")
	seedCluster(t, st, "cls-upd", "cust-upd")

	createResp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId: "cust-upd", ClusterId: "cls-upd", Namespace: "old-ns", ReleaseName: "upd-rel", Enabled: true,
		},
	))
	require.NoError(t, err)
	defID := createResp.Msg.Definition.Id

	updateResp, err := svc.UpdateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.UpdateReleaseDefinitionRequest{
			DefinitionId:    defID,
			Namespace:       "new-ns",
			ReleaseName:     "upd-rel-renamed",
			ExpectedVersion: 1,
		},
	))
	require.NoError(t, err)
	assert.Equal(t, "new-ns", updateResp.Msg.Definition.Namespace)
	assert.Equal(t, "upd-rel-renamed", updateResp.Msg.Definition.ReleaseName)
	assert.Equal(t, int64(2), updateResp.Msg.Definition.Version)
}

func TestUpdateReleaseDefinition_OptimisticLockConflict(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-lock", "lock")
	seedCluster(t, st, "cls-lock", "cust-lock")

	createResp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId: "cust-lock", ClusterId: "cls-lock", Namespace: "ns", ReleaseName: "lock-rel", Enabled: true,
		},
	))
	require.NoError(t, err)
	defID := createResp.Msg.Definition.Id

	// AC-040-04: submitting old version → optimistic_lock_conflict.
	_, err = svc.UpdateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.UpdateReleaseDefinitionRequest{
			DefinitionId:    defID,
			Namespace:       "ns-2",
			ExpectedVersion: 999,
		},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "optimistic_lock_conflict")
}

func TestUpdateReleaseDefinition_NotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.UpdateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.UpdateReleaseDefinitionRequest{DefinitionId: "no-such", ExpectedVersion: 1},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// ── DisableReleaseDefinition ───────────────────────────────

func TestDisableReleaseDefinition_Success(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-dis", "dis")
	seedCluster(t, st, "cls-dis", "cust-dis")

	createResp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId: "cust-dis", ClusterId: "cls-dis", Namespace: "prod", ReleaseName: "dis-rel", Enabled: true,
		},
	))
	require.NoError(t, err)
	defID := createResp.Msg.Definition.Id

	disableResp, err := svc.DisableReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.DisableReleaseDefinitionRequest{DefinitionId: defID},
	))
	require.NoError(t, err)
	assert.Equal(t, "disabled", disableResp.Msg.Definition.Status)

	// Verify event persisted.
	events, err := st.DefinitionEvents().List(context.Background(), defID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(events), 2) // create + disable
	found := false
	for _, e := range events {
		if e.EventType == "definition_disabled" {
			found = true
		}
	}
	assert.True(t, found, "definition_disabled event not found")
}

func TestDisableReleaseDefinition_Idempotent(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-idem", "idem")
	seedCluster(t, st, "cls-idem", "cust-idem")

	createResp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId: "cust-idem", ClusterId: "cls-idem", Namespace: "ns", ReleaseName: "idem-rel", Enabled: true,
		},
	))
	require.NoError(t, err)
	defID := createResp.Msg.Definition.Id

	_, err = svc.DisableReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.DisableReleaseDefinitionRequest{DefinitionId: defID},
	))
	require.NoError(t, err)

	// Second disable — should be a no-op.
	_, err = svc.DisableReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.DisableReleaseDefinitionRequest{DefinitionId: defID},
	))
	require.NoError(t, err)

	// Events should still only have one disable.
	events, err := st.DefinitionEvents().List(context.Background(), defID)
	require.NoError(t, err)
	disableCount := 0
	for _, e := range events {
		if e.EventType == "definition_disabled" {
			disableCount++
		}
	}
	assert.Equal(t, 1, disableCount, "idempotent disable must not emit duplicate events")
}

// ── Disabled definition rejects operations (AC-040-03) ─────────────

func TestCreateOperation_DisabledDefinition(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-opd", "opd")
	seedCluster(t, st, "cls-opd", "cust-opd")

	createResp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId: "cust-opd", ClusterId: "cls-opd", Namespace: "ns2", ReleaseName: "opd-rel2", Enabled: true,
		},
	))
	require.NoError(t, err)

	// Disable it.
	_, err = svc.DisableReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.DisableReleaseDefinitionRequest{DefinitionId: createResp.Msg.Definition.Id},
	))
	require.NoError(t, err)

	// Try create operation on disabled definition.
	_, err = svc.CreateOperation(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateOperationRequest{
			OperationType:       "INSTALL",
			ReleaseDefinitionId: createResp.Msg.Definition.Id,
			IdempotencyKey:      "idis-001",
		},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "release_definition_disabled")
}

func TestCreateReleaseDefinition_NoPhysicalDelete(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-nodel", "nodel")
	seedCluster(t, st, "cls-nodel", "cust-nodel")

	createResp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId: "cust-nodel", ClusterId: "cls-nodel", Namespace: "ns", ReleaseName: "nodel-rel", Enabled: true,
		},
	))
	require.NoError(t, err)
	defID := createResp.Msg.Definition.Id

	// Disable (soft state change, no DELETE).
	_, err = svc.DisableReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.DisableReleaseDefinitionRequest{DefinitionId: defID},
	))
	require.NoError(t, err)

	// Definition must still be retrievable after disable.
	getResp, err := svc.GetReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.GetReleaseDefinitionRequest{DefinitionId: defID},
	))
	require.NoError(t, err)
	assert.Equal(t, "disabled", getResp.Msg.Definition.Status, "definition was physically deleted")
}

func TestCreateOperation_ValidationFlow(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-flow", "flow")
	seedCluster(t, st, "cls-flow", "cust-flow")

	ctx := context.Background()

	// Create definition → create operation → verify
	createDefResp, err := svc.CreateReleaseDefinition(ctx, connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId: "cust-flow", ClusterId: "cls-flow", Namespace: "flow-ns", ReleaseName: "flow-rel", ChartName: "nginx", Enabled: true,
			Actor: &commonv1.ActorContext{UserId: "actor-1"},
		},
	))
	require.NoError(t, err)
	defID := createDefResp.Msg.Definition.Id

	// Seed approved values revision for INSTALL validation.
	seedValuesRevision(t, st, "vr-flow", defID, store.ValuesStatusApproved)
	createOpResp, err := svc.CreateOperation(ctx, connect.NewRequest(
		&orchestratorv1.CreateOperationRequest{
			OperationType:       "INSTALL",
			ReleaseDefinitionId: defID,
			BundleId:            "bundle-flow",
			ValuesRevisionId:    "vr-flow",
			IdempotencyKey:      "flow-key",
			Actor:               &commonv1.ActorContext{UserId: "actor-1"},
		},
	))
	require.NoError(t, err)
	assert.NotEmpty(t, createOpResp.Msg.OperationId)
	assert.Equal(t, "preflight", createOpResp.Msg.State)
}
