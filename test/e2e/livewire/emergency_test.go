package livewire

import (
	"context"
	"errors"
	"testing"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	e2e "github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"
)

func TestWorkloadReferenceUsesAcceptedGVRSpellings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		kind      string
		namespace string
		workload  string
		want      string
		wantErr   bool
	}{
		{name: "deployment", kind: "Deployment", namespace: "ns", workload: "app", want: "deployments/ns/app"},
		{name: "statefulset", kind: "StatefulSet", namespace: "ns", workload: "db", want: "statefulsets/ns/db"},
		{name: "daemonset", kind: "DaemonSet", namespace: "ns", workload: "agent", want: "daemonsets/ns/agent"},
		// The server's accepted GVR set is closed; anything else must fail
		// here rather than be sent as an unsupported reference.
		{name: "unsupported kind", kind: "CronJob", namespace: "ns", workload: "job", wantErr: true},
		{name: "missing namespace", kind: "Deployment", namespace: " ", workload: "app", wantErr: true},
		{name: "missing name", kind: "Deployment", namespace: "ns", workload: "", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := workloadReference(test.kind, test.namespace, test.workload)
			if test.wantErr {
				if err == nil {
					t.Fatalf("workloadReference() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("workloadReference() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("workloadReference() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTargetsResolveGVRReference(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setEmergencyTargets(&orchestratorv1.EmergencyTarget{
		WorkloadRef:     &orchestratorv1.WorkloadRef{Kind: "Deployment", Name: "release-fixture", Namespace: "release-fixture"},
		CurrentReplicas: 3,
	})

	targets, err := h.connector.Targets(context.Background(), "e2e-emergency-target")
	if err != nil {
		t.Fatalf("Targets() error = %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("Targets() = %d targets, want 1", len(targets))
	}
	target := targets[0]
	if target.Reference != "deployments/release-fixture/release-fixture" {
		t.Fatalf("Reference = %q, want the plural GVR form", target.Reference)
	}
	// The diagnostic key keeps the kind form and must never be what is sent.
	if target.WorkloadKey() != "deployment/release-fixture/release-fixture" {
		t.Fatalf("WorkloadKey() = %q", target.WorkloadKey())
	}
	if target.CurrentReplicas != 3 {
		t.Fatalf("CurrentReplicas = %d, want 3", target.CurrentReplicas)
	}
}

func TestTargetsFailClosedOnUnsupportedKind(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setEmergencyTargets(&orchestratorv1.EmergencyTarget{
		WorkloadRef: &orchestratorv1.WorkloadRef{Kind: "Job", Name: "j", Namespace: "ns"},
	})
	if _, err := h.connector.Targets(context.Background(), "e2e-emergency-target"); err == nil {
		t.Fatal("Targets() error = nil, want a fail-closed error for an unsupported kind")
	}
}

func TestSetReplicasSendsReplicaBranchContract(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	reference, err := h.connector.SetReplicas(context.Background(), stages.EmergencySetReplicasRequest{
		DefinitionID: "e2e-emergency-target",
		WorkloadRef:  "deployments/release-fixture/release-fixture",
		Replicas:     5,
		Convergence:  stages.EmergencyConvergenceRevertOnNextReconcile,
	})
	if err != nil {
		t.Fatalf("SetReplicas() error = %v", err)
	}
	if reference.ID != "op-emergency" {
		t.Fatalf("SetReplicas() id = %q", reference.ID)
	}
	if reference.Convergence != stages.EmergencyConvergenceRevertOnNextReconcile {
		t.Fatalf("Convergence = %q", reference.Convergence)
	}

	request := h.orch.emergencyRequest(0)
	if request.GetSetReplicas() != 5 {
		t.Fatalf("set_replicas = %d, want 5", request.GetSetReplicas())
	}
	if request.GetConvergenceStrategy() != orchestratorv1.ConvergenceStrategy_REVERT_ON_NEXT_RECONCILE {
		t.Fatalf("convergence_strategy = %v", request.GetConvergenceStrategy())
	}
	// The image branch fields are mutually exclusive with set_replicas; sending
	// either would be rejected as conflicting_change.
	if request.GetArtifactRef() != "" || request.GetContainer() != "" {
		t.Fatalf("image-branch fields must not be set: artifact=%q container=%q", request.GetArtifactRef(), request.GetContainer())
	}
	// The server computes the replay fingerprint itself and never reads this
	// field, so a client-supplied value could only cause a false conflict.
	if len(request.GetRequestHash()) != 0 {
		t.Fatalf("request_hash = %x, want unset", request.GetRequestHash())
	}
	// Qualified by the requested replica count: replaying the same change
	// dedupes, while a different replica count is a different write the server
	// must not refuse as a same-key conflict.
	if request.GetIdempotencyKey() != "e2e-emergency-set-replicas-e2e-emergency-target-5" {
		t.Fatalf("idempotency_key = %q", request.GetIdempotencyKey())
	}
}

func TestSetReplicasRejectsUnacceptedChange(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setEmergencyResponse(&orchestratorv1.ExecuteEmergencyChangeResponse{
		OperationId: "op-emergency",
		Result:      &orchestratorv1.EmergencyResult{Requested: false},
	})
	if _, err := h.connector.SetReplicas(context.Background(), stages.EmergencySetReplicasRequest{
		DefinitionID: "d", WorkloadRef: "deployments/ns/app", Replicas: 2,
	}); err == nil {
		t.Fatal("SetReplicas() error = nil, want an error when the change was not accepted")
	}
}

func TestSetReplicasRejectsMissingOperationID(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setEmergencyResponse(&orchestratorv1.ExecuteEmergencyChangeResponse{
		Result: &orchestratorv1.EmergencyResult{Requested: true},
	})
	if _, err := h.connector.SetReplicas(context.Background(), stages.EmergencySetReplicasRequest{
		DefinitionID: "d", WorkloadRef: "deployments/ns/app", Replicas: 2,
	}); err == nil {
		t.Fatal("SetReplicas() error = nil, want an error when no operation id is returned")
	}
}

func TestSetReplicasValidatesInputBeforeWriting(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	if _, err := h.connector.SetReplicas(context.Background(), stages.EmergencySetReplicasRequest{DefinitionID: "d", Replicas: 2}); err == nil {
		t.Fatal("SetReplicas() error = nil, want an error for a missing workload reference")
	}
	if _, err := h.connector.SetReplicas(context.Background(), stages.EmergencySetReplicasRequest{DefinitionID: "d", WorkloadRef: "deployments/ns/app", Replicas: -1}); err == nil {
		t.Fatal("SetReplicas() error = nil, want an error for a negative replica count")
	}
	if got := h.orch.emergencyRequestCount(); got != 0 {
		t.Fatalf("emergency requests = %d, want 0 rejected locally", got)
	}
}

func TestConvergenceMappingRoundTrip(t *testing.T) {
	t.Parallel()

	// An unknown policy must fail closed rather than silently becoming a
	// persistent REQUIRE_PROMOTION change.
	if got := convergenceStrategyFor(""); got != orchestratorv1.ConvergenceStrategy_CONVERGENCE_STRATEGY_UNSPECIFIED {
		t.Fatalf("convergenceStrategyFor(\"\") = %v, want UNSPECIFIED", got)
	}
	if got := convergenceStrategyFor(stages.EmergencyConvergenceRequirePromotion); got != orchestratorv1.ConvergenceStrategy_REQUIRE_PROMOTION {
		t.Fatalf("convergenceStrategyFor(REQUIRE_PROMOTION) = %v", got)
	}
	if got := convergencePolicyName(orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REQUIRE_PROMOTION); got != stages.EmergencyConvergenceRequirePromotion {
		t.Fatalf("convergencePolicyName(REQUIRE_PROMOTION) = %q", got)
	}
	if got := convergencePolicyName(orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_UNSPECIFIED); got != "" {
		t.Fatalf("convergencePolicyName(UNSPECIFIED) = %q, want empty", got)
	}
}

func TestAwaitOperationReportsEmergencyConvergence(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setOperations(map[string]*orchestratorv1.Operation{
		"op-emergency": {
			OperationId:         "op-emergency",
			ReleaseDefinitionId: "e2e-emergency-target",
			OperationType:       "EMERGENCY",
			State:               orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED,
		},
	})
	h.orch.setEmergencyResults(map[string]*orchestratorv1.EmergencyResult{
		"op-emergency": {ConvergencePolicy: orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REVERT_ON_NEXT_RECONCILE},
	})

	reference, err := h.connector.AwaitOperation(context.Background(), "op-emergency")
	if err != nil {
		t.Fatalf("AwaitOperation() error = %v", err)
	}
	if !reference.Succeeded() {
		t.Fatalf("AwaitOperation() = %+v, want succeeded", reference)
	}
	if reference.Convergence != stages.EmergencyConvergenceRevertOnNextReconcile {
		t.Fatalf("Convergence = %q, want the canonical policy name", reference.Convergence)
	}
}

// replicaObserverFake satisfies the read-only observation seam for the
// integration test; the client-go implementation is a separate concern. It is
// function-backed so the test can make the observed cluster state follow the
// applied change instead of returning a constant.
type replicaObserverFake struct {
	observe func(namespace, workloadName string) (stages.ReplicaObservation, error)
}

func (f *replicaObserverFake) ObserveReplicas(_ context.Context, namespace, workloadName string) (stages.ReplicaObservation, error) {
	if f.observe == nil {
		return stages.ReplicaObservation{}, errors.New("no observation")
	}
	return f.observe(namespace, workloadName)
}

// TestEmergencyStageRunsOverConnectAdapters proves the emergency writer seam
// end to end: the real stage takes over, writes through the Connect adapter,
// observes the effect, and registers exactly one restore compensation.
func TestEmergencyStageRunsOverConnectAdapters(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setInventory(inventoryRow("e2e-emergency-target", 1, nil))
	h.orch.setEmergencyTargets(&orchestratorv1.EmergencyTarget{
		WorkloadRef:     &orchestratorv1.WorkloadRef{Kind: "Deployment", Name: "release-fixture", Namespace: "release-fixture"},
		CurrentReplicas: 3,
	})
	h.orch.setEmergencyResponse(&orchestratorv1.ExecuteEmergencyChangeResponse{
		OperationId: "op-emergency",
		Result: &orchestratorv1.EmergencyResult{
			Requested:         true,
			ConvergencePolicy: orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REVERT_ON_NEXT_RECONCILE,
		},
	})
	h.orch.setOperations(map[string]*orchestratorv1.Operation{
		"op-emergency": {
			OperationId:         "op-emergency",
			ReleaseDefinitionId: "e2e-emergency-target",
			OperationType:       "EMERGENCY",
			State:               orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED,
		},
	})
	h.orch.setEmergencyResults(map[string]*orchestratorv1.EmergencyResult{
		"op-emergency": {ConvergencePolicy: orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REVERT_ON_NEXT_RECONCILE},
	})

	// The observed cluster state follows the last accepted change, so the stage
	// sees a real deviation and then a real restore.
	observer := &replicaObserverFake{observe: func(_, workloadName string) (stages.ReplicaObservation, error) {
		replicas := h.orch.lastEmergencyReplicas(3)
		return stages.ReplicaObservation{Workload: workloadName, Replicas: replicas, Ready: replicas}, nil
	}}
	registry := e2e.NewCompensationRegistry()
	stage := stages.NewEmergencyStage(h.connector, observer, registry, stages.WriteTarget{
		Name:         "emergency",
		DefinitionID: "e2e-emergency-target",
	}, 5)

	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("EmergencyStage.Run() error = %v", err)
	}
	// The change must be a visible deviation from the observed baseline.
	if got := h.orch.emergencyRequestCount(); got != 1 {
		t.Fatalf("emergency requests = %d, want 1", got)
	}
	if got := h.orch.emergencyRequest(0).GetSetReplicas(); got != 5 {
		t.Fatalf("emergency set_replicas = %d, want the requested deviation 5", got)
	}

	// Exactly one compensation, and running it must restore the baseline.
	if err := registry.Run(context.Background()); err != nil {
		t.Fatalf("registry.Run() error = %v", err)
	}
	if got := h.orch.emergencyRequestCount(); got != 2 {
		t.Fatalf("emergency requests after compensation = %d, want 2", got)
	}
	if got := h.orch.emergencyRequest(1).GetSetReplicas(); got != 3 {
		t.Fatalf("restore replica count = %d, want the baseline 3", got)
	}
	if got := h.orch.emergencyRequest(1).GetWorkloadRef(); got != "deployments/release-fixture/release-fixture" {
		t.Fatalf("restore workload_ref = %q", got)
	}
}
