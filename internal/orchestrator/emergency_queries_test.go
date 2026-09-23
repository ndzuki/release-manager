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
	seedEmergencyImageIdentity(t, st)
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

// AC-081-01 (D1=B) + AC-085-01: ListEmergencyTargets derives a real
// EmergencyTarget from release_inventory + release_definitions instead of the
// removed manifest_inventory_unavailable stub. With the authoritative
// workload identity persisted (REQ-085), kind/uid are non-empty and equal to
// it; without identity the D7=A sentinels apply (empty workload_ref.kind/uid,
// current_replicas=-1, empty containers/annotations/image refs).
func TestListEmergencyTargetsDerivesFromInventory(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)

	resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
		ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTargets(), 1)
	target := resp.Msg.GetTargets()[0]

	// Derived values (release_inventory + release_definitions).
	assert.False(t, target.GetHpaManaged())
	assert.Equal(t, int32(10), target.GetMaxEmergencyReplicas())
	require.Len(t, target.GetPromotions(), 1)
	assert.Equal(t, workloadDeployment, target.GetPromotions()[0].GetWorkloadKind())
	assert.Equal(t, "replicas", target.GetPromotions()[0].GetField())
	assert.Equal(t, "replicaCount", target.GetPromotions()[0].GetValuesPath())

	// AC-085-01: the authoritative identity wins over the D1=B derivation.
	assert.Equal(t, workloadDeployment, target.GetWorkloadRef().GetKind())
	assert.Equal(t, "my-release", target.GetWorkloadRef().GetName())
	assert.Equal(t, "default", target.GetWorkloadRef().GetNamespace())
	assert.Equal(t, "uid-replicas-0001", target.GetWorkloadRef().GetUid())

	// D7=A unavailable sentinels (identity-independent fields).
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

// AC-085-01 (negative): an inventory row without the operator-reported
// identity keeps the D1=B derivation — name/namespace from the row and empty
// kind/uid (downstream fail-closed).
func TestListEmergencyTargetsDerivesWithoutIdentity(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
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

	assert.Equal(t, "my-release", target.GetWorkloadRef().GetName())
	assert.Equal(t, "default", target.GetWorkloadRef().GetNamespace())
	assert.Empty(t, target.GetWorkloadRef().GetKind())
	assert.Empty(t, target.GetWorkloadRef().GetUid())
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

// ── W4: operator observation projection + fail-closed staleness ─────────────

// seedObservedInventory persists the operator-reported observation on the
// def-001 inventory row through the real store write path (TASK-168 W3): an
// inventory sync Upsert creates the row, then UpdateWorkloadObservation writes
// the observation exactly as the operator report path does. Production never
// writes observed values through Upsert, so neither do these tests.
func seedObservedInventory(t *testing.T, st store.Store, observation emergencyObservation) {
	t.Helper()
	require.NoError(t, st.Inventories().Upsert(t.Context(), &store.ReleaseInventory{
		ReleaseDefinitionID: "def-001", CustomerID: "cust-001", ClusterID: "cls-001",
		Namespace: "default", ReleaseName: "my-release", Status: "deployed", InventoryStatus: store.InventoryActive,
		WorkloadKind: workloadDeployment, WorkloadName: "my-release", WorkloadNamespace: "default", WorkloadUID: "uid-observed-0001",
	}))
	reported := store.WorkloadObservation{
		Containers: observation.Containers,
		ImageRefs:  observation.CurrentImageRefs,
		ObservedAt: observation.ObservedAt,
	}
	if observation.ReplicasObserved {
		replicas := observation.CurrentReplicas
		reported.Replicas = &replicas
	}
	require.NoError(t, st.Inventories().UpdateWorkloadObservation(t.Context(), "cust-001", "cls-001", "default", "my-release", reported))
}

// freshObservation is a complete, in-window observation for def-001's workload.
func freshObservation(now time.Time) emergencyObservation {
	return emergencyObservation{
		Containers:       []string{"api", "sidecar"},
		CurrentImageRefs: map[string]string{"api": "registry.example/team/api:1.0.0", "sidecar": "registry.example/team/sidecar:v2"},
		CurrentReplicas:  3,
		ReplicasObserved: true,
		ObservedAt:       now,
	}
}

func TestEmergencyObservedWorkload(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	replicas := int32(0)
	t.Run("nil row is not observed", func(t *testing.T) {
		observation, observed := emergencyObservedWorkload(nil)
		assert.False(t, observed)
		assert.Equal(t, emergencyObservation{}, observation)
	})
	t.Run("zero observed_at is not observed", func(t *testing.T) {
		observation, observed := emergencyObservedWorkload(&store.ReleaseInventory{
			ObservedContainers: []string{"api"},
			ObservedImageRefs:  map[string]string{"api": "registry.example/team/api:1.0.0"},
			ObservedReplicas:   &replicas,
		})
		assert.False(t, observed)
		assert.Equal(t, emergencyObservation{}, observation)
	})
	t.Run("nil replicas keeps replicas unobserved", func(t *testing.T) {
		observation, observed := emergencyObservedWorkload(&store.ReleaseInventory{
			ObservedContainers: []string{"api"},
			ObservedImageRefs:  map[string]string{"api": "registry.example/team/api:1.0.0"},
			ObservedAt:         now,
		})
		require.True(t, observed)
		assert.False(t, observation.ReplicasObserved)
		assert.Equal(t, int32(0), observation.CurrentReplicas)
	})
	t.Run("full observation is projected", func(t *testing.T) {
		observation, observed := emergencyObservedWorkload(&store.ReleaseInventory{
			ObservedContainers: []string{"api", "sidecar"},
			ObservedImageRefs:  map[string]string{"api": "registry.example/team/api:1.0.0"},
			ObservedReplicas:   &replicas,
			ObservedAt:         now,
		})
		require.True(t, observed)
		assert.Equal(t, []string{"api", "sidecar"}, observation.Containers)
		assert.Equal(t, map[string]string{"api": "registry.example/team/api:1.0.0"}, observation.CurrentImageRefs)
		assert.True(t, observation.ReplicasObserved)
		assert.Equal(t, int32(0), observation.CurrentReplicas)
		assert.Equal(t, now, observation.ObservedAt)
	})
}

func TestEmergencyObservationObservedFresh(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{name: "zero timestamp is never fresh", at: time.Time{}, want: false},
		{name: "just observed", at: now, want: true},
		{name: "inside window", at: now.Add(-14 * time.Minute), want: true},
		{name: "exactly at the 15 minute boundary", at: now.Add(-15 * time.Minute), want: true},
		{name: "one nanosecond past the boundary", at: now.Add(-15*time.Minute - time.Nanosecond), want: false},
		{name: "well past the window", at: now.Add(-16 * time.Minute), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, emergencyObservation{ObservedAt: tc.at}.observedFresh(now))
		})
	}
}

func TestProjectEmergencyWorkload(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	t.Run("missing observation keeps the sentinels", func(t *testing.T) {
		view := projectEmergencyWorkload(emergencyObservation{}, false, now)
		assert.False(t, view.Observed)
		assert.Empty(t, view.Containers)
		assert.Empty(t, view.CurrentImageRefs)
		assert.Equal(t, int32(-1), view.CurrentReplicas)
		assert.False(t, view.imageSelectable())
	})
	t.Run("stale observation is not projected", func(t *testing.T) {
		observation := freshObservation(now.Add(-16 * time.Minute))
		view := projectEmergencyWorkload(observation, true, now)
		assert.False(t, view.Observed)
		assert.Empty(t, view.Containers)
		assert.Empty(t, view.CurrentImageRefs)
		assert.Equal(t, int32(-1), view.CurrentReplicas)
	})
	t.Run("fresh observation is projected", func(t *testing.T) {
		view := projectEmergencyWorkload(freshObservation(now), true, now)
		assert.True(t, view.Observed)
		assert.Equal(t, []string{"api", "sidecar"}, view.Containers)
		assert.Equal(t, map[string]string{"api": "registry.example/team/api:1.0.0", "sidecar": "registry.example/team/sidecar:v2"}, view.CurrentImageRefs)
		assert.Equal(t, int32(3), view.CurrentReplicas)
		assert.True(t, view.imageSelectable())
	})
	t.Run("observed zero replicas is kept", func(t *testing.T) {
		observation := freshObservation(now)
		observation.CurrentReplicas = 0
		view := projectEmergencyWorkload(observation, true, now)
		assert.Equal(t, int32(0), view.CurrentReplicas)
	})
	t.Run("unobserved replicas (daemonset) keeps -1 even when fresh", func(t *testing.T) {
		observation := freshObservation(now)
		observation.ReplicasObserved = false
		view := projectEmergencyWorkload(observation, true, now)
		assert.True(t, view.Observed)
		assert.Equal(t, int32(-1), view.CurrentReplicas)
	})
	t.Run("unobserved view never advertises image", func(t *testing.T) {
		// Guards the invariant explicitly: image operations require the
		// observed marker, not merely populated fields.
		view := emergencyWorkloadView{
			Containers:       []string{"api"},
			CurrentImageRefs: map[string]string{"api": "registry.example/team/api:1.0.0"},
			CurrentReplicas:  -1,
		}
		assert.False(t, view.imageSelectable())
	})
	t.Run("projection does not alias the observation slices", func(t *testing.T) {
		observation := freshObservation(now)
		view := projectEmergencyWorkload(observation, true, now)
		observation.Containers[0] = "mutated"
		observation.CurrentImageRefs["api"] = "mutated"
		assert.Equal(t, []string{"api", "sidecar"}, view.Containers)
		assert.Equal(t, "registry.example/team/api:1.0.0", view.CurrentImageRefs["api"])
	})
}

// W4 presence semantics: an observed real 0 replicas is projected as 0, while
// an unobserved replica count (e.g. a DaemonSet) keeps the -1 sentinel even
// when the rest of the observation is fresh.
func TestListEmergencyTargetsReplicasPresence(t *testing.T) {
	t.Run("observed zero replicas stays zero", func(t *testing.T) {
		svc, st, cleanup := setupService(t)
		defer cleanup()
		seedDefinition(t, st)
		observation := freshObservation(time.Now().UTC())
		observation.CurrentReplicas = 0
		seedObservedInventory(t, st, observation)

		resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
			ReleaseDefinitionId: "def-001",
		}))
		require.NoError(t, err)
		require.Len(t, resp.Msg.GetTargets(), 1)
		assert.Equal(t, int32(0), resp.Msg.GetTargets()[0].GetCurrentReplicas())
		assert.Equal(t, []string{"api", "sidecar"}, resp.Msg.GetTargets()[0].GetContainers())
	})
	t.Run("unobserved replicas keeps the sentinel", func(t *testing.T) {
		svc, st, cleanup := setupService(t)
		defer cleanup()
		seedDefinition(t, st)
		observation := freshObservation(time.Now().UTC())
		observation.ReplicasObserved = false
		seedObservedInventory(t, st, observation)

		resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
			ReleaseDefinitionId: "def-001",
		}))
		require.NoError(t, err)
		require.Len(t, resp.Msg.GetTargets(), 1)
		assert.Equal(t, int32(-1), resp.Msg.GetTargets()[0].GetCurrentReplicas())
		// The container/image part of the same observation is still projected.
		assert.Equal(t, []string{"api", "sidecar"}, resp.Msg.GetTargets()[0].GetContainers())
	})
}

// AC-058-09: a fresh observation advertises SET_CONTAINER_IMAGE and fills the
// real current values, while annotation operations stay degraded.
func TestListEmergencyTargetsProjectsFreshObservation(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)
	seedObservedInventory(t, st, freshObservation(time.Now().UTC()))

	resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
		ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTargets(), 1)
	target := resp.Msg.GetTargets()[0]

	assert.Equal(t, []string{"api", "sidecar"}, target.GetContainers())
	assert.Equal(t, "registry.example/team/api:1.0.0", target.GetCurrentImageRefs()["api"])
	assert.Equal(t, int32(3), target.GetCurrentReplicas())
	// The annotation data plane is out of scope this round: no value is ever
	// projected and no annotation operation is advertised.
	assert.Empty(t, target.GetCurrentAnnotations())
	assert.Equal(t, []orchestratorv1.EmergencyAction{
		orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS,
		orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE,
	}, target.GetSupportedOperations())
	assert.NotContains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_APPROVED_ANNOTATION)
}

// W4 fail-closed: a stale observation must never be projected as the current
// value. Removing the 15-minute staleness check makes this test fail.
func TestListEmergencyTargetsFailsClosedOnStaleObservation(t *testing.T) {
	for _, age := range []time.Duration{15*time.Minute + time.Second, 16 * time.Minute, time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			svc, st, cleanup := setupService(t)
			defer cleanup()
			seedDefinition(t, st)
			seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)
			seedObservedInventory(t, st, freshObservation(time.Now().UTC().Add(-age)))

			resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
				ReleaseDefinitionId: "def-001",
			}))
			require.NoError(t, err)
			require.Len(t, resp.Msg.GetTargets(), 1)
			target := resp.Msg.GetTargets()[0]

			assert.Empty(t, target.GetContainers())
			assert.Empty(t, target.GetCurrentImageRefs())
			assert.Equal(t, int32(-1), target.GetCurrentReplicas())
			assert.NotContains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE)
			assert.Contains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS)
		})
	}
}

// W4 fail-closed: no observation (old operator, or a row without observed
// fields) keeps every mutable sentinel.
func TestListEmergencyTargetsFailsClosedWithoutObservation(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)

	resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
		ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTargets(), 1)
	target := resp.Msg.GetTargets()[0]

	assert.Empty(t, target.GetContainers())
	assert.Empty(t, target.GetCurrentImageRefs())
	assert.Empty(t, target.GetCurrentAnnotations())
	assert.Equal(t, int32(-1), target.GetCurrentReplicas())
	assert.NotContains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE)
}

// AC-058-09: image availability must not be coupled to replicas eligibility —
// an HPA-managed definition still offers the image change when a fresh
// observation exists, and only that one.
func TestListEmergencyTargetsImageOperationIndependentOfReplicas(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), true, 10)
	seedObservedInventory(t, st, freshObservation(time.Now().UTC()))

	resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
		ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTargets(), 1)
	assert.Equal(t, []orchestratorv1.EmergencyAction{
		orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE,
	}, resp.Msg.GetTargets()[0].GetSupportedOperations())
}

// ── W5: E3 repository-scoped candidate artifacts ────────────────────────────

func seedCandidateImage(t *testing.T, st store.Store, id, ref, digest string) {
	t.Helper()
	validatedAt := time.Now().UTC()
	require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
		ID: id, ArtifactType: store.ArtifactImage, Ref: ref, Digest: digest,
		ValidatedAt: &validatedAt, SourceID: "source-" + id,
	}))
}

// AC-058-10: with a fresh observation the candidate list only contains
// artifacts from the requested container's current logical repository. The
// comparison is by logical repository, so a tag-form current ref matches both
// tag-form and digest-form candidates from the same repository.
func TestListCandidateArtifactsFiltersByTargetRepository(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCandidateImage(t, st, "artifact-same-repo-tag", "registry.example/team/api:2.0.0", "sha256:tagged")
	seedCandidateImage(t, st, "artifact-same-repo-digest", "registry.example/team/api@sha256:deadbeef", "sha256:pinned")
	seedCandidateImage(t, st, "artifact-other-repo", "registry.example/other/api:1.0.0", "sha256:other")
	require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
		ID: "artifact-chart", ArtifactType: store.ArtifactChart,
		Ref: "registry.example/team/api:9.9.9", Digest: "sha256:chart",
		ValidatedAt: ptrTime(time.Now().UTC()), SourceID: "source-chart",
	}))
	seedObservedInventory(t, st, freshObservation(time.Now().UTC()))

	resp, err := svc.ListCandidateArtifacts(deployerCtx(), connect.NewRequest(&orchestratorv1.ListCandidateArtifactsRequest{
		OrganizationId: "org-001", ReleaseDefinitionId: "def-001",
		WorkloadRef: "deployments/default/my-release", Container: "api",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetArtifacts(), 2)
	ids := []string{resp.Msg.GetArtifacts()[0].GetId(), resp.Msg.GetArtifacts()[1].GetId()}
	assert.ElementsMatch(t, []string{"artifact-same-repo-tag", "artifact-same-repo-digest"}, ids)
	for _, artifact := range resp.Msg.GetArtifacts() {
		assert.Equal(t, "registry.example/team/api", artifact.GetRepository())
	}
}

// E3 fail-closed: an inventory row without an observation cannot yield a
// repository, so the scoped list is empty rather than silently unscoped.
func TestListCandidateArtifactsFailsClosedWithoutObservation(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	// The row exists but carries no observed fields (an older operator).
	require.NoError(t, st.Inventories().Upsert(t.Context(), &store.ReleaseInventory{
		ReleaseDefinitionID: "def-001", CustomerID: "cust-001", ClusterID: "cls-001",
		Namespace: "default", ReleaseName: "my-release", Status: "deployed", InventoryStatus: store.InventoryActive,
	}))
	seedCandidateImage(t, st, "artifact-image", "registry.example/team/api:2.0.0", "sha256:same")

	resp, err := svc.ListCandidateArtifacts(deployerCtx(), connect.NewRequest(&orchestratorv1.ListCandidateArtifactsRequest{
		OrganizationId: "org-001", ReleaseDefinitionId: "def-001",
		WorkloadRef: "deployments/default/my-release", Container: "api",
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetArtifacts())
}

// E3 fail-closed: a stale observation must not be used to derive the
// repository. Removing the 15-minute staleness check makes this test fail.
func TestListCandidateArtifactsFailsClosedOnStaleObservation(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCandidateImage(t, st, "artifact-same-repo", "registry.example/team/api:2.0.0", "sha256:same")
	seedObservedInventory(t, st, freshObservation(time.Now().UTC().Add(-16*time.Minute)))

	resp, err := svc.ListCandidateArtifacts(deployerCtx(), connect.NewRequest(&orchestratorv1.ListCandidateArtifactsRequest{
		OrganizationId: "org-001", ReleaseDefinitionId: "def-001",
		WorkloadRef: "deployments/default/my-release", Container: "api",
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetArtifacts())
}

// E3 fail-closed: a container the observation does not cover has no derivable
// repository, so nothing is returned.
func TestListCandidateArtifactsFailsClosedWhenContainerNotObserved(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedCandidateImage(t, st, "artifact-image", "registry.example/team/api:2.0.0", "sha256:same")
	seedObservedInventory(t, st, freshObservation(time.Now().UTC()))

	resp, err := svc.ListCandidateArtifacts(deployerCtx(), connect.NewRequest(&orchestratorv1.ListCandidateArtifactsRequest{
		OrganizationId: "org-001", ReleaseDefinitionId: "def-001",
		WorkloadRef: "deployments/default/my-release", Container: "not-observed",
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetArtifacts())
}

// candidateArtifactSummaries is the projection boundary: only validated IMAGE
// artifacts are candidates, and a scoped request narrows them to one logical
// repository. Kept as a pure function so the guard stays falsifiable without a
// store.
func TestCandidateArtifactSummaries(t *testing.T) {
	validatedAt := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	validatedImage := func(id, ref string) *store.CandidateArtifact {
		return &store.CandidateArtifact{
			ID: id, ArtifactType: store.ArtifactImage, Ref: ref,
			Digest: "sha256:" + id, ValidatedAt: &validatedAt, SourceID: "source-" + id,
		}
	}
	unvalidatedImage := &store.CandidateArtifact{
		ID: "unvalidated", ArtifactType: store.ArtifactImage,
		Ref: "registry.example/team/api:1.0.0", Digest: "sha256:unvalidated", SourceID: "source-unvalidated",
	}
	validatedChart := &store.CandidateArtifact{
		ID: "chart", ArtifactType: store.ArtifactChart,
		Ref: "registry.example/team/api:1.0.0", Digest: "sha256:chart", ValidatedAt: &validatedAt, SourceID: "source-chart",
	}

	t.Run("unscoped keeps every validated image", func(t *testing.T) {
		summaries := candidateArtifactSummaries([]*store.CandidateArtifact{
			validatedImage("a", "registry.example/team/api:1.0.0"),
			validatedImage("b", "registry.example/other/api:1.0.0"),
			unvalidatedImage,
			validatedChart,
		}, "", false)
		require.Len(t, summaries, 2)
		assert.Equal(t, "a", summaries[0].GetId())
		assert.Equal(t, "b", summaries[1].GetId())
	})
	t.Run("scoped keeps only the same logical repository", func(t *testing.T) {
		summaries := candidateArtifactSummaries([]*store.CandidateArtifact{
			validatedImage("same-repo", "registry.example/team/api@sha256:abc"),
			validatedImage("other-repo", "registry.example/other/api:1.0.0"),
			unvalidatedImage,
			validatedChart,
		}, "registry.example/team/api", true)
		require.Len(t, summaries, 1)
		assert.Equal(t, "same-repo", summaries[0].GetId())
		assert.Equal(t, "registry.example/team/api", summaries[0].GetRepository())
	})
	t.Run("scoped with an underivable repository filters everything out", func(t *testing.T) {
		summaries := candidateArtifactSummaries([]*store.CandidateArtifact{
			validatedImage("a", "registry.example/team/api:1.0.0"),
		}, "", true)
		assert.Empty(t, summaries)
	})
	t.Run("unvalidated artifacts are never candidates", func(t *testing.T) {
		// G11 ruling ②: selection is "was this artifact validated?", so the
		// projection must drop an unvalidated row even if a store ever returned
		// one.
		summaries := candidateArtifactSummaries([]*store.CandidateArtifact{unvalidatedImage}, "", false)
		assert.Empty(t, summaries)
	})
}

func TestEmergencyArtifactRepositoryScope(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	fresh := projectEmergencyWorkload(freshObservation(now), true, now)
	stale := projectEmergencyWorkload(freshObservation(now.Add(-16*time.Minute)), true, now)
	tests := []struct {
		name       string
		container  string
		workload   emergencyWorkloadView
		wantRepo   string
		wantScoped bool
	}{
		{name: "no container stays unscoped", container: "", workload: fresh, wantRepo: "", wantScoped: false},
		{name: "blank container stays unscoped", container: "   ", workload: fresh, wantRepo: "", wantScoped: false},
		{name: "observed container scopes to its repository", container: "api", workload: fresh, wantRepo: "registry.example/team/api", wantScoped: true},
		{name: "observed container with digest ref", container: "sidecar", workload: fresh, wantRepo: "registry.example/team/sidecar", wantScoped: true},
		{name: "no observation scopes to nothing", container: "api", workload: emergencyWorkloadView{}, wantRepo: "", wantScoped: true},
		{name: "stale observation scopes to nothing", container: "api", workload: stale, wantRepo: "", wantScoped: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repository, scoped := emergencyArtifactRepositoryScope(tc.container, tc.workload)
			assert.Equal(t, tc.wantRepo, repository)
			assert.Equal(t, tc.wantScoped, scoped)
		})
	}
}

func TestEmergencyRepository(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{name: "digest reference", ref: "registry.example/team/api@sha256:abc", want: "registry.example/team/api"},
		{name: "tag reference", ref: "registry.example/team/api:1.0.0", want: "registry.example/team/api"},
		{name: "registry port is not a tag", ref: "registry.example:5000/team/api:1.0.0", want: "registry.example:5000/team/api"},
		{name: "untagged reference", ref: "registry.example/team/api", want: "registry.example/team/api"},
		{name: "single segment", ref: "api:1.0.0", want: "api"},
		{name: "digest with tag", ref: "registry.example/team/api:1.0.0@sha256:abc", want: "registry.example/team/api"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, emergencyRepository(tc.ref))
		})
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

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
