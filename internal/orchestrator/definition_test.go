package orchestrator

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
	"google.golang.org/protobuf/proto"
)

func seedCustomer(t *testing.T, st store.Store, id, name string) {
	t.Helper()
	c := &store.Customer{ID: id, Name: name, Slug: id}
	require.NoError(t, createCustomerViaManagement(context.Background(), st, c))
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
			// REQ-040: the creating actor's organization owns the definition.
			Actor: &commonv1.ActorContext{UserId: "user-1", Organization: "org-1"},
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
	if assert.NotNil(t, got.OwnerOrganizationID, "creating actor's organization must own the definition (REQ-040)") {
		assert.Equal(t, "org-1", *got.OwnerOrganizationID)
	}
	assert.Equal(t, store.DefStatusActive, got.Status)
	assert.NotEmpty(t, got.ID)

	// Verify domain event persisted.
	events, err := definitionEventsByDefinition(t, st, def.Id)
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
			Namespace:       ptr("new-ns"),
			ReleaseName:     ptr("upd-rel-renamed"),
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
			Namespace:       ptr("ns-2"),
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
	events, err := definitionEventsByDefinition(t, st, defID)
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
	events, err := definitionEventsByDefinition(t, st, defID)
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
	seedActorBinding(t, st, "cust-opd")

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
	_, err = svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(
		&orchestratorv1.CreateOperationRequest{
			OperationType:       "INSTALL",
			ReleaseDefinitionId: createResp.Msg.Definition.Id,
		},
	), "idis-001"))
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
	seedActorBinding(t, st, "cust-flow")

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
	seedBundle(t, st, "bundle-flow")
	linkCurrentBundle(t, st, defID, "bundle-flow")
	createOpResp, err := svc.CreateOperation(adminCtx(), withIdempotencyKey(connect.NewRequest(
		&orchestratorv1.CreateOperationRequest{
			OperationType:       "INSTALL",
			ReleaseDefinitionId: defID,
			BundleId:            "bundle-flow",
			ValuesRevisionId:    "vr-flow",
		},
	), "flow-key"))
	require.NoError(t, err)
	assert.NotEmpty(t, createOpResp.Msg.OperationId)
	assert.Equal(t, "preflight", createOpResp.Msg.State)
}

func seedBundle(t *testing.T, st store.Store, id string) {
	t.Helper()
	require.NoError(t, st.Bundles().Create(context.Background(), &store.ReleaseBundle{
		ID:           id,
		Name:         id,
		DigestAlg:    "sha256",
		DigestValue:  "digest-" + id,
		Status:       store.BundleValidated,
		ChartRef:     "oci://registry.example.com/charts/nginx",
		ChartVersion: "1.0.0",
		Images: []store.BundleImage{{
			Ref:        "registry.example.com/release:" + id,
			Digest:     "sha256:image-" + id,
			ValuesPath: "image",
		}},
	}))
}

// ptr builds the pointer an `optional` proto3 field decodes into, so a test can say
// "present" (the pointer is non-nil, even when it points at "") rather than only "non-empty".
func ptr[T any](value T) *T { return &value }

// TASK-214: presence must decide the outcome. These three cases are the contract: absent
// leaves the stored value alone, present-and-empty CLEARS it, present-and-set replaces it.
// Before the fields were optional, the middle case silently kept the old value while the
// call still answered 200.
func TestUpdateReleaseDefinition_PresenceSemantics(t *testing.T) {
	tests := []struct {
		name      string
		request   *orchestratorv1.UpdateReleaseDefinitionRequest
		wantNS    string
		wantRel   string
		wantChart string
	}{
		{
			name:      "absent fields leave the stored values alone",
			request:   &orchestratorv1.UpdateReleaseDefinitionRequest{},
			wantNS:    "ns-presence",
			wantRel:   "rel-presence",
			wantChart: "chart-presence",
		},
		{
			name:      "present and empty clears",
			request:   &orchestratorv1.UpdateReleaseDefinitionRequest{Namespace: ptr(""), ReleaseName: ptr(""), ChartName: ptr("")},
			wantNS:    "",
			wantRel:   "",
			wantChart: "",
		},
		{
			name:      "present and set replaces",
			request:   &orchestratorv1.UpdateReleaseDefinitionRequest{Namespace: ptr("ns-next"), ReleaseName: ptr("rel-next"), ChartName: ptr("chart-next")},
			wantNS:    "ns-next",
			wantRel:   "rel-next",
			wantChart: "chart-next",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, st, cleanup := setupService(t)
			defer cleanup()
			seedCustomer(t, st, "cust-presence", "presence")
			seedCluster(t, st, "cls-presence", "cust-presence")

			createResp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
				&orchestratorv1.CreateReleaseDefinitionRequest{
					CustomerId: "cust-presence", ClusterId: "cls-presence",
					Namespace: "ns-presence", ReleaseName: "rel-presence", ChartName: "chart-presence", Enabled: true,
				},
			))
			require.NoError(t, err)

			tt.request.DefinitionId = createResp.Msg.Definition.Id
			tt.request.ExpectedVersion = 1
			updateResp, err := svc.UpdateReleaseDefinition(context.Background(), connect.NewRequest(tt.request))
			require.NoError(t, err)
			assert.Equal(t, tt.wantNS, updateResp.Msg.Definition.Namespace)
			assert.Equal(t, tt.wantRel, updateResp.Msg.Definition.ReleaseName)
			assert.Equal(t, tt.wantChart, updateResp.Msg.Definition.ChartName)
		})
	}
}

// The repeated fields cannot express presence, so the wrappers carry it: `items: []` is a
// deliberate clear, a non-empty wrapper replaces, and the wrapper wins over the legacy
// field. The legacy field itself keeps working (replacing with at least one entry).
func TestUpdateReleaseDefinition_ListReplacementAndClearing(t *testing.T) {
	newServiceWithMapping := func(t *testing.T) (*Service, string) {
		t.Helper()
		svc, st, cleanup := setupService(t)
		t.Cleanup(cleanup)
		seedCustomer(t, st, "cust-lists", "lists")
		seedCluster(t, st, "cls-lists", "cust-lists")
		createResp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
			&orchestratorv1.CreateReleaseDefinitionRequest{
				CustomerId: "cust-lists", ClusterId: "cls-lists", Namespace: "ns", ReleaseName: "rel", Enabled: true,
				PromotionMappings: []*orchestratorv1.PromotionMapping{{WorkloadKind: "Deployment", WorkloadName: "api", Container: "api", Field: "image", ValuesPath: "image.tag"}},
			},
		))
		require.NoError(t, err)
		require.Len(t, decodePromotionMappings(t, createResp.Msg.Definition.PromotionMappings), 1, "the fixture must start with a mapping")
		return svc, createResp.Msg.Definition.Id
	}

	t.Run("absent wrapper and absent legacy list leave the list alone", func(t *testing.T) {
		svc, defID := newServiceWithMapping(t)
		resp, err := svc.UpdateReleaseDefinition(context.Background(), connect.NewRequest(
			&orchestratorv1.UpdateReleaseDefinitionRequest{DefinitionId: defID, ExpectedVersion: 1},
		))
		require.NoError(t, err)
		assert.Len(t, decodePromotionMappings(t, resp.Msg.Definition.PromotionMappings), 1)
	})

	t.Run("empty wrapper clears the list", func(t *testing.T) {
		svc, defID := newServiceWithMapping(t)
		resp, err := svc.UpdateReleaseDefinition(context.Background(), connect.NewRequest(
			&orchestratorv1.UpdateReleaseDefinitionRequest{
				DefinitionId: defID, ExpectedVersion: 1,
				PromotionMappingsReplace: &orchestratorv1.PromotionMappingList{},
			},
		))
		require.NoError(t, err)
		assert.Empty(t, decodePromotionMappings(t, resp.Msg.Definition.PromotionMappings), "an empty wrapper must clear, not be ignored")
	})

	t.Run("wrapper wins over the legacy field", func(t *testing.T) {
		svc, defID := newServiceWithMapping(t)
		resp, err := svc.UpdateReleaseDefinition(context.Background(), connect.NewRequest(
			&orchestratorv1.UpdateReleaseDefinitionRequest{
				DefinitionId: defID, ExpectedVersion: 1,
				PromotionMappings: []*orchestratorv1.PromotionMapping{{WorkloadKind: "Deployment", WorkloadName: "api", Container: "api", Field: "image", ValuesPath: "legacy.path"}},
				PromotionMappingsReplace: &orchestratorv1.PromotionMappingList{
					Items: []*orchestratorv1.PromotionMapping{{WorkloadKind: "Deployment", WorkloadName: "api", Container: "api", Field: "image", ValuesPath: "wrapper.path"}},
				},
			},
		))
		require.NoError(t, err)
		mappings := decodePromotionMappings(t, resp.Msg.Definition.PromotionMappings)
		require.Len(t, mappings, 1)
		assert.Equal(t, "wrapper.path", mappings[0].ValuesPath)
	})

	t.Run("legacy non-empty list still replaces", func(t *testing.T) {
		svc, defID := newServiceWithMapping(t)
		resp, err := svc.UpdateReleaseDefinition(context.Background(), connect.NewRequest(
			&orchestratorv1.UpdateReleaseDefinitionRequest{
				DefinitionId: defID, ExpectedVersion: 1,
				PromotionMappings: []*orchestratorv1.PromotionMapping{{WorkloadKind: "Deployment", WorkloadName: "api", Container: "api", Field: "image", ValuesPath: "legacy.path"}},
			},
		))
		require.NoError(t, err)
		mappings := decodePromotionMappings(t, resp.Msg.Definition.PromotionMappings)
		require.Len(t, mappings, 1)
		assert.Equal(t, "legacy.path", mappings[0].ValuesPath)
	})

	t.Run("empty wrapper clears the annotation keys too", func(t *testing.T) {
		svc, st, cleanup := setupService(t)
		defer cleanup()
		seedCustomer(t, st, "cust-keys", "keys")
		seedCluster(t, st, "cls-keys", "cust-keys")
		createResp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
			&orchestratorv1.CreateReleaseDefinitionRequest{
				CustomerId: "cust-keys", ClusterId: "cls-keys", Namespace: "ns", ReleaseName: "rel", Enabled: true,
				ApprovedAnnotationKeys: []*orchestratorv1.ApprovedAnnotationKey{{Key: "team", Scope: "example.com/team"}},
			},
		))
		require.NoError(t, err)
		require.Len(t, decodeAnnotationKeys(t, createResp.Msg.Definition.ApprovedAnnotationKeys), 1)

		resp, err := svc.UpdateReleaseDefinition(context.Background(), connect.NewRequest(
			&orchestratorv1.UpdateReleaseDefinitionRequest{
				DefinitionId: createResp.Msg.Definition.Id, ExpectedVersion: 1,
				ApprovedAnnotationKeysReplace: &orchestratorv1.ApprovedAnnotationKeyList{},
			},
		))
		require.NoError(t, err)
		assert.Empty(t, decodeAnnotationKeys(t, resp.Msg.Definition.ApprovedAnnotationKeys))
	})
}

// The response carries these two as JSON-encoded bytes (common.v1.ReleaseDefinition), so
// assertions decode them; an absent or cleared list decodes to an empty slice.
func decodePromotionMappings(t *testing.T, raw []byte) []store.PromotionMapping {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var out []store.PromotionMapping
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func decodeAnnotationKeys(t *testing.T, raw []byte) []store.ApprovedAnnotationKey {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var out []store.ApprovedAnnotationKey
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// TASK-214 fixes a WIRE-level false success: the request fields have to survive
// serialization for a clear to reach the server at all. The other tests construct Go
// structs directly, which cannot catch a presence that is lost on the wire (an
// `optional` removed from the proto, a codegen change, a client that omits the field).
func TestUpdateReleaseDefinition_PresenceSurvivesTheWire(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedCustomer(t, st, "cust-wire", "wire")
	seedCluster(t, st, "cls-wire", "cust-wire")

	createResp, err := svc.CreateReleaseDefinition(context.Background(), connect.NewRequest(
		&orchestratorv1.CreateReleaseDefinitionRequest{
			CustomerId: "cust-wire", ClusterId: "cls-wire", Namespace: "ns-wire", ReleaseName: "rel-wire",
			ChartName: "chart-wire", Enabled: true,
			PromotionMappings: []*orchestratorv1.PromotionMapping{{WorkloadKind: "Deployment", WorkloadName: "api", Field: "image", ValuesPath: "image.tag"}},
		},
	))
	require.NoError(t, err)
	definitionID := createResp.Msg.Definition.Id

	// What the console actually sends when the operator clears everything.
	sent := &orchestratorv1.UpdateReleaseDefinitionRequest{
		DefinitionId:             definitionID,
		ExpectedVersion:          1,
		Namespace:                ptr(""),
		ChartName:                ptr(""),
		PromotionMappingsReplace: &orchestratorv1.PromotionMappingList{},
	}
	encoded, err := proto.Marshal(sent)
	require.NoError(t, err)

	decoded := &orchestratorv1.UpdateReleaseDefinitionRequest{}
	require.NoError(t, proto.Unmarshal(encoded, decoded))
	require.NotNil(t, decoded.Namespace, "an empty namespace must survive the wire as PRESENT")
	require.NotNil(t, decoded.ChartName)
	require.Nil(t, decoded.ReleaseName, "a field the caller never set must stay absent")
	// An empty wrapper has to stay present, otherwise the server reads "leave it alone".
	require.NotNil(t, decoded.PromotionMappingsReplace)

	updateResp, err := svc.UpdateReleaseDefinition(context.Background(), connect.NewRequest(decoded))
	require.NoError(t, err)
	assert.Empty(t, updateResp.Msg.Definition.Namespace)
	assert.Empty(t, updateResp.Msg.Definition.ChartName)
	assert.Equal(t, "rel-wire", updateResp.Msg.Definition.ReleaseName, "absent fields stay untouched")
	assert.Empty(t, decodePromotionMappings(t, updateResp.Msg.Definition.PromotionMappings))

	// And the response has to say "empty list", not "null": the console treats a
	// non-array JSON payload as a contract violation and would show an error banner.
	assert.JSONEq(t, "[]", string(updateResp.Msg.Definition.PromotionMappings))
}

// definitionEventRow mirrors the columns these assertions use: DefinitionEventStore.List was
// removed with the TASK-226 dead-surface batch (no shipping caller), so the fixture reads the
// rows directly.
type definitionEventRow struct {
	DefinitionID string
	EventType    string
}

func definitionEventsByDefinition(t *testing.T, st store.Store, definitionID string) ([]definitionEventRow, error) {
	t.Helper()
	rows, err := testSQLDB(t, st).QueryContext(context.Background(),
		`SELECT definition_id, event_type FROM release_definition_events WHERE definition_id = ? ORDER BY created_at, id`,
		definitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []definitionEventRow
	for rows.Next() {
		var row definitionEventRow
		if err := rows.Scan(&row.DefinitionID, &row.EventType); err != nil {
			return nil, err
		}
		events = append(events, row)
	}
	return events, rows.Err()
}
