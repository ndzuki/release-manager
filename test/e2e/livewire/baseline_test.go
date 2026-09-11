package livewire

import (
	"context"
	"testing"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// TestBaselineReplicasSamplesEmergencyTargets covers the pre-run sample cleanup
// restores from (AC-066-23/34): the recorded reference must be the authoritative
// plural GVR form a later restore sends, the order must be deterministic, and a
// scaled-to-zero workload must survive as a real baseline.
func TestBaselineReplicasSamplesEmergencyTargets(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setEmergencyTargets(
		&orchestratorv1.EmergencyTarget{
			WorkloadRef:     &orchestratorv1.WorkloadRef{Kind: "Deployment", Name: "release-fixture", Namespace: "release-fixture"},
			CurrentReplicas: 3,
		},
		&orchestratorv1.EmergencyTarget{
			WorkloadRef:     &orchestratorv1.WorkloadRef{Kind: "Deployment", Name: "alpha", Namespace: "release-fixture"},
			CurrentReplicas: 0,
		},
	)

	refs, err := BaselineReplicas(context.Background(), h.cfg)
	if err != nil {
		t.Fatalf("BaselineReplicas() error = %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("BaselineReplicas() = %+v, want both targets", refs)
	}
	if refs[0].WorkloadRef != "deployments/release-fixture/alpha" {
		t.Fatalf("refs[0].WorkloadRef = %q, want the plural GVR form", refs[0].WorkloadRef)
	}
	if refs[1].WorkloadRef != "deployments/release-fixture/release-fixture" {
		t.Fatalf("refs[1].WorkloadRef = %q, want deterministic ordering", refs[1].WorkloadRef)
	}
	if refs[0].Replicas != 0 {
		t.Fatalf("refs[0].Replicas = %d, want 0 preserved", refs[0].Replicas)
	}
	if refs[1].Replicas != 3 {
		t.Fatalf("refs[1].Replicas = %d, want 3", refs[1].Replicas)
	}
	// The recorded definition must be the server-minted id the write path
	// expects, not the fixture's logical key.
	if want := "33333333-3333-3333-3333-333333333333"; refs[0].ReleaseDefinitionID != want {
		t.Fatalf("ReleaseDefinitionID = %q, want the seeded emergency definition %q", refs[0].ReleaseDefinitionID, want)
	}
}

// TestBaselineReplicasWithoutTargets proves an empty observation yields no
// fabricated rows: cleanup must see an empty baseline and degrade explicitly.
func TestBaselineReplicasWithoutTargets(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	refs, err := BaselineReplicas(context.Background(), h.cfg)
	if err != nil {
		t.Fatalf("BaselineReplicas() error = %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("BaselineReplicas() = %+v, want none", refs)
	}
}

func TestBaselineReplicasRejectsNilConfig(t *testing.T) {
	t.Parallel()

	if _, err := BaselineReplicas(context.Background(), nil); err == nil {
		t.Fatal("BaselineReplicas(nil) error = nil, want a programming-error rejection")
	}
}
