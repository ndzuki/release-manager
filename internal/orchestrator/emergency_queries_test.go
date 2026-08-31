package orchestrator

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

func TestCheckEmergencyConflictReturnsRunningStandardOperation(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	require.NoError(t, st.Operations().Create(t.Context(), &store.Operation{
		ID: uuid.NewString(), OperationType: store.OperationUpgrade, Status: store.StatusRunning,
		ReleaseDefinitionID: "def-001", IdempotencyKey: uuid.NewString(), RequestHash: "hash",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))

	resp, err := svc.CheckEmergencyConflict(deployerCtx(), connect.NewRequest(&orchestratorv1.CheckEmergencyConflictRequest{
		ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetHasConflict())
	assert.Equal(t, "UPGRADE", resp.Msg.GetRunningOperation().GetType())
	assert.Equal(t, string(store.StatusRunning), resp.Msg.GetRunningOperation().GetStatus())
}

func TestListCandidateArtifactsReturnsValidatedImages(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	validatedAt := time.Now().UTC()
	require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
		ID: "artifact-image", ArtifactType: store.ArtifactImage,
		Ref: "registry.example/team/api@sha256:abc", Digest: "sha256:abc",
		ValidatedAt: &validatedAt, SourceID: "source-1",
	}))
	require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
		ID: "artifact-chart", ArtifactType: store.ArtifactChart,
		Ref: "registry.example/charts/api:1.0.0", Digest: "sha256:def",
		ValidatedAt: &validatedAt, SourceID: "source-2",
	}))

	resp, err := svc.ListCandidateArtifacts(deployerCtx(), connect.NewRequest(&orchestratorv1.ListCandidateArtifactsRequest{
		OrganizationId: "org-001", ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetArtifacts(), 1)
	assert.Equal(t, "artifact-image", resp.Msg.GetArtifacts()[0].GetId())
	assert.Equal(t, "registry.example/team/api", resp.Msg.GetArtifacts()[0].GetRepository())
}

// AC-079-G5: ListCandidateArtifacts cascade parameters require workload_ref.
func TestListCandidateArtifactsCascadeRequiresWorkloadRef(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	for _, msg := range []*orchestratorv1.ListCandidateArtifactsRequest{
		{OrganizationId: "org-001", ReleaseDefinitionId: "def-001", Container: "api"},
		{OrganizationId: "org-001", ReleaseDefinitionId: "def-001", OperationVersion: "v1.0.0"},
	} {
		_, err := svc.ListCandidateArtifacts(deployerCtx(), connect.NewRequest(msg))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Equal(t, "workload_ref_required", connectErrorReason(err))
	}
	// With workload_ref present the cascade validation passes.
	_, err := svc.ListCandidateArtifacts(deployerCtx(), connect.NewRequest(&orchestratorv1.ListCandidateArtifactsRequest{
		OrganizationId: "org-001", ReleaseDefinitionId: "def-001",
		WorkloadRef: "deployments/default/api", Container: "api",
	}))
	require.NoError(t, err)
}

func TestListConvergenceTasksReturnsPendingTask(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	svc, _, _ = emergencyTestServiceFromExisting(t, svc, st)
	definition, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	definition.PromotionMappings = []store.PromotionMapping{{
		WorkloadKind: workloadDeployment, WorkloadName: "api", Container: "api",
		Field: "image_digest", ValuesPath: "api.image.digest",
	}}
	_, err = st.Definitions().Update(t.Context(), definition, nil)
	require.NoError(t, err)
	req := emergencyImageRequest("convergence-list")
	req.Msg.ConvergenceStrategy = orchestratorv1.ConvergenceStrategy_REQUIRE_PROMOTION
	req.Msg.TargetLocks = []string{"api.image.digest"}
	created, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.NoError(t, err)

	resp, err := svc.ListConvergenceTasks(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.ListConvergenceTasksRequest{
		ReleaseDefinitionId: "def-001", StatusFilter: "pending_promotion",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTasks(), 1)
	assert.Equal(t, created.Msg.GetResult().GetConvergenceTasks()[0].GetTaskId(), resp.Msg.GetTasks()[0].GetTaskId())
	assert.True(t, resp.Msg.GetTasks()[0].GetSelectable())
	assert.Equal(t, []string{"api.image.digest"}, resp.Msg.GetTasks()[0].GetPromotionPaths())
}

// AC-081-01 (D1=B): ListEmergencyTargets derives a real EmergencyTarget from
// release_inventory + release_definitions instead of the removed
// manifest_inventory_unavailable stub. Underivable fields carry the D7=A
// unavailable sentinels (current_replicas=-1, empty containers/annotations/
// image refs, empty workload_ref.kind/uid).
func TestListEmergencyTargetsDerivesFromInventory(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)
	require.NoError(t, st.Inventories().Upsert(t.Context(), &store.ReleaseInventory{
		ReleaseDefinitionID: "def-001", CustomerID: "cust-001", ClusterID: "cls-001",
		Namespace: "default", ReleaseName: "my-release", Status: "deployed", InventoryStatus: store.InventoryActive,
	}))

	resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
		ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTargets(), 1)
	target := resp.Msg.GetTargets()[0]

	// Derived values (release_inventory + release_definitions).
	assert.Equal(t, "my-release", target.GetWorkloadRef().GetName())
	assert.Equal(t, "default", target.GetWorkloadRef().GetNamespace())
	assert.False(t, target.GetHpaManaged())
	assert.Equal(t, int32(10), target.GetMaxEmergencyReplicas())
	require.Len(t, target.GetPromotions(), 1)
	assert.Equal(t, workloadDeployment, target.GetPromotions()[0].GetWorkloadKind())
	assert.Equal(t, "replicas", target.GetPromotions()[0].GetField())
	assert.Equal(t, "replicaCount", target.GetPromotions()[0].GetValuesPath())

	// D7=A unavailable sentinels.
	assert.Empty(t, target.GetWorkloadRef().GetKind())
	assert.Empty(t, target.GetWorkloadRef().GetUid())
	assert.Empty(t, target.GetContainers())
	assert.Empty(t, target.GetCurrentImageRefs())
	assert.Empty(t, target.GetCurrentAnnotations())
	assert.Equal(t, int32(-1), target.GetCurrentReplicas())

	// supported_operations: full computation (D1=B 待澄清③) — replicas is
	// supported for a non-HPA DEPLOYMENT mapping; image/annotation operations
	// stay degraded (no container/annotation data).
	assert.Contains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS)
	assert.NotContains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE)
	assert.NotContains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_APPROVED_ANNOTATION)
}

// AC-081-01: a missing inventory row is not an error — the response carries
// zero targets.
func TestListEmergencyTargetsEmptyWhenInventoryMissing(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
		ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetTargets())
}

// AC-081-01: supported_operations degrades without promotion mappings (no
// derivable workload kind) and drops SET_REPLICAS for HPA-managed
// definitions (REQ-032 §171).
func TestListEmergencyTargetsSupportedOperations(t *testing.T) {
	t.Run("no promotion mappings", func(t *testing.T) {
		svc, st, cleanup := setupService(t)
		defer cleanup()
		seedDefinition(t, st)
		require.NoError(t, st.Inventories().Upsert(t.Context(), &store.ReleaseInventory{
			ReleaseDefinitionID: "def-001", CustomerID: "cust-001", ClusterID: "cls-001",
			Namespace: "default", ReleaseName: "my-release", InventoryStatus: store.InventoryActive,
		}))
		resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
			ReleaseDefinitionId: "def-001",
		}))
		require.NoError(t, err)
		require.Len(t, resp.Msg.GetTargets(), 1)
		assert.Empty(t, resp.Msg.GetTargets()[0].GetSupportedOperations())
	})
	t.Run("hpa managed", func(t *testing.T) {
		svc, st, cleanup := setupService(t)
		defer cleanup()
		seedDefinition(t, st)
		seedReplicasDefinition(t, st, replicasPromotionMapping(), true, 10)
		require.NoError(t, st.Inventories().Upsert(t.Context(), &store.ReleaseInventory{
			ReleaseDefinitionID: "def-001", CustomerID: "cust-001", ClusterID: "cls-001",
			Namespace: "default", ReleaseName: "my-release", InventoryStatus: store.InventoryActive,
		}))
		resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
			ReleaseDefinitionId: "def-001",
		}))
		require.NoError(t, err)
		require.Len(t, resp.Msg.GetTargets(), 1)
		assert.Empty(t, resp.Msg.GetTargets()[0].GetSupportedOperations())
	})
	t.Run("zero replicas ceiling", func(t *testing.T) {
		// max_emergency_replicas=0 leaves no legal replicas value, so the
		// derived operations must not advertise SET_REPLICAS.
		svc, st, cleanup := setupService(t)
		defer cleanup()
		seedDefinition(t, st)
		seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 0)
		require.NoError(t, st.Inventories().Upsert(t.Context(), &store.ReleaseInventory{
			ReleaseDefinitionID: "def-001", CustomerID: "cust-001", ClusterID: "cls-001",
			Namespace: "default", ReleaseName: "my-release", InventoryStatus: store.InventoryActive,
		}))
		resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
			ReleaseDefinitionId: "def-001",
		}))
		require.NoError(t, err)
		require.Len(t, resp.Msg.GetTargets(), 1)
		assert.Empty(t, resp.Msg.GetTargets()[0].GetSupportedOperations())
	})
}

// AC-081-01 failure paths: missing definition id and unknown definition are
// rejected with the existing emergency error contract.
func TestListEmergencyTargetsFailurePaths(t *testing.T) {
	t.Run("missing definition id", func(t *testing.T) {
		svc, st, cleanup := setupService(t)
		defer cleanup()
		seedDefinition(t, st)
		_, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Equal(t, "release_definition_id_required", connectErrorReason(err))
	})
	t.Run("definition not found", func(t *testing.T) {
		svc, st, cleanup := setupService(t)
		defer cleanup()
		seedDefinition(t, st)
		_, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
			ReleaseDefinitionId: "nonexistent",
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		assert.Equal(t, "definition_not_found", connectErrorReason(err))
	})
	t.Run("unauthenticated", func(t *testing.T) {
		svc, st, cleanup := setupService(t)
		defer cleanup()
		seedDefinition(t, st)
		_, err := svc.ListEmergencyTargets(context.Background(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
			ReleaseDefinitionId: "def-001",
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})
}

func emergencyTestServiceFromExisting(t *testing.T, svc *Service, st store.Store) (*Service, store.Store, *recordingEmergencyDispatcher) {
	t.Helper()
	require.NoError(t, st.Users().Create(t.Context(), &store.User{
		ID: "release-admin", Username: "release-admin", Status: store.UserActive,
	}))
	require.NoError(t, st.OrgMembers().Create(t.Context(), &store.OrganizationMember{
		OrgID: "org-001", UserID: "release-admin", Role: store.RoleReleaseAdmin,
	}))
	require.NoError(t, st.Clusters().Create(t.Context(), &store.Cluster{ID: "cls-001", Name: "cls-001", CustomerID: "cust-001"}))
	require.NoError(t, st.Operators().Create(t.Context(), &store.Operator{ID: "op-001", Name: "op-001", CustomerID: "cust-001", ClusterID: "cls-001"}))
	require.NoError(t, st.Sessions().Create(t.Context(), &store.Session{
		ID: uuid.NewString(), OperatorID: "op-001", Status: store.SessionOnline, InstanceID: "inst-1",
		StartedAt: time.Now().UTC(), LastHeartbeat: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	dispatcher := &recordingEmergencyDispatcher{}
	svc.emergencyDispatcher = dispatcher
	// Canonical contract fixtures: kill switch on + validated trusted image.
	require.NoError(t, st.EmergencyConfig().SetEmergencyConfig(t.Context(), store.EmergencyConfig{Enabled: true}))
	seedEmergencyTestArtifact(t, st)
	return svc, st, dispatcher
}
