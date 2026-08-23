package orchestrator

import (
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

func TestListEmergencyTargetsReportsUnavailable(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	_, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
		ReleaseDefinitionId: "def-001",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Equal(t, "manifest_inventory_unavailable", connectErrorReason(err))
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
