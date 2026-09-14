package main

import (
	"encoding/json"
	"testing"

	"github.com/ndzuki/release-manager/test/e2e"
)

// TestBaselineArtifactRoundTripsReplicaBaseline locks the write/read contract
// between the run-side baseline artifact and cleanup's recovery projection: a
// replica sampled before the run must survive the artifact encoding and reach
// cleanup as a restore target (AC-066-23/34). The two ends are separate types in
// separate packages, so only an explicit round trip proves they still agree.
func TestBaselineArtifactRoundTripsReplicaBaseline(t *testing.T) {
	t.Parallel()

	artifact := baselineArtifact{RunID: "run-round-trip"}
	artifact.Identity.WorkloadReplicas = []e2e.WorkloadReplicaRef{
		{ReleaseDefinitionID: "def-emergency", WorkloadRef: "deployments/ns/name", Replicas: 2},
		{ReleaseDefinitionID: "def-emergency", WorkloadRef: "deployments/ns/other", Replicas: 0},
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("marshal baseline artifact: %v", err)
	}
	var decoded baselineArtifact
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal baseline artifact: %v", err)
	}

	recovery := e2e.BaselineRecoveryFromSnapshots(decoded.FixtureSnapshot)
	if len(recovery.Replicas) != 2 {
		t.Fatalf("recovery replicas = %+v, want both sampled rows (encoded: %s)", recovery.Replicas, encoded)
	}
	if recovery.Replicas[0].WorkloadRef != "deployments/ns/name" || recovery.Replicas[0].Replicas != 2 {
		t.Fatalf("recovery replicas[0] = %+v, want deployments/ns/name with 2", recovery.Replicas[0])
	}
	// A scaled-to-zero workload is a real baseline, not a missing value.
	if recovery.Replicas[1].WorkloadRef != "deployments/ns/other" || recovery.Replicas[1].Replicas != 0 {
		t.Fatalf("recovery replicas[1] = %+v, want deployments/ns/other with 0", recovery.Replicas[1])
	}
}
