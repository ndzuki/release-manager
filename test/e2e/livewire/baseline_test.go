package livewire

import (
	"context"
	"errors"
	"testing"

	e2e "github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"
)

// baselineSamplerStub stands in for the read-only cluster observation. The API
// target list deliberately carries the D7=A sentinel (-1) rather than a count, so
// the baseline must never read its replicas from there.
type baselineSamplerStub struct {
	replicas map[string]int32
	err      error
}

func (s baselineSamplerStub) ObserveReplicas(_ context.Context, cluster, namespace, workloadName string) (stages.ReplicaObservation, error) {
	if s.err != nil {
		return stages.ReplicaObservation{}, s.err
	}
	return stages.ReplicaObservation{Replicas: s.replicas[cluster+"/"+namespace+"/"+workloadName]}, nil
}

// emergencyTargetsForBaseline returns targets whose CurrentReplicas is the live
// unavailable sentinel, exactly as ListEmergencyTargets reports them. Reference
// is the adapter-resolved plural GVR form the real connector sets, and Cluster is
// the customer cluster the workload runs in.
func emergencyTargetsForBaseline(names ...string) []stages.EmergencyTarget {
	targets := make([]stages.EmergencyTarget, 0, len(names))
	for _, name := range names {
		targets = append(targets, stages.EmergencyTarget{
			WorkloadKind:    "Deployment",
			WorkloadName:    name,
			Namespace:       "release-fixture",
			CurrentReplicas: -1,
			Cluster:         "dev-customer-a-direct",
			Reference:       "deployments/release-fixture/" + name,
		})
	}
	return targets
}

// TestBaselineReplicasSamplesEmergencyTargets covers the pre-run sample cleanup
// restores from (AC-066-23/34): the recorded reference must be the authoritative
// plural GVR form a later restore sends, the order must be deterministic, and a
// scaled-to-zero workload must survive as a real baseline.
//
// The counts come from the observer, never from the target's CurrentReplicas:
// that field is a live D7=A unavailable sentinel, and reading it recorded no
// replicas at all (real smoke 2026-09-11).
func TestBaselineReplicasSamplesEmergencyTargets(t *testing.T) {
	t.Parallel()

	const definitionID = "33333333-3333-3333-3333-333333333333"
	targets := emergencyTargetsForBaseline("release-fixture", "alpha")
	// Keyed by cluster too: a baseline that ignored the cluster would look up
	// nothing and silently record zero rows.
	sampler := baselineSamplerStub{replicas: map[string]int32{
		"dev-customer-a-direct/release-fixture/release-fixture": 3,
		"dev-customer-a-direct/release-fixture/alpha":           0,
	}}

	refs, err := sampleReplicas(context.Background(), definitionID, targets, sampler)
	if err != nil {
		t.Fatalf("sampleReplicas() error = %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("sampleReplicas() = %+v, want both targets", refs)
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
	if refs[0].ReleaseDefinitionID != definitionID {
		t.Fatalf("ReleaseDefinitionID = %q, want the seeded emergency definition %q", refs[0].ReleaseDefinitionID, definitionID)
	}
}

// TestBaselineReplicasSkipsAnUnavailableObservation proves a sentinel count is
// never recorded as a real baseline: a fabricated row would make cleanup restore
// a replica count nobody observed.
func TestBaselineReplicasSkipsAnUnavailableObservation(t *testing.T) {
	t.Parallel()

	targets := emergencyTargetsForBaseline("release-fixture")
	sampler := baselineSamplerStub{replicas: map[string]int32{
		"dev-customer-a-direct/release-fixture/release-fixture": -1,
	}}

	refs, err := sampleReplicas(context.Background(), "def-emergency", targets, sampler)
	if err != nil {
		t.Fatalf("sampleReplicas() error = %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("sampleReplicas() = %+v, want the unavailable observation dropped", refs)
	}
}

// TestBaselineReplicasSurfacesAnObservationFailure proves a read failure is
// reported rather than silently recorded as an empty baseline.
func TestBaselineReplicasSurfacesAnObservationFailure(t *testing.T) {
	t.Parallel()

	targets := emergencyTargetsForBaseline("release-fixture")
	sampler := baselineSamplerStub{err: errors.New("private detail")}

	if _, err := sampleReplicas(context.Background(), "def-emergency", targets, sampler); err == nil {
		t.Fatal("sampleReplicas() error = nil, want the observation failure surfaced")
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

// TestInventoryRefsFromReleaseDigestsKeepsOnlyAddressableRows proves the revision
// baseline carries exactly what cleanup can act on, so the recovery target stops
// being empty and skipped_revision_restore stops firing for every row.
func TestInventoryRefsFromReleaseDigestsKeepsOnlyAddressableRows(t *testing.T) {
	t.Parallel()

	refs := inventoryRefsFromReleaseDigests([]e2e.ReleaseDigest{
		{ReleaseDefinitionID: "def-b", Revision: 2, ValuesDigest: "digest-b"},
		{ReleaseDefinitionID: "def-a", Revision: 1, ValuesDigest: "digest-a"},
		// A row with no definition id or no revision names no rollback target.
		{ReleaseDefinitionID: "", Revision: 3, ValuesDigest: "digest-x"},
		{ReleaseDefinitionID: "def-c", Revision: 0, ValuesDigest: "digest-c"},
	})
	if len(refs) != 2 {
		t.Fatalf("inventoryRefsFromReleaseDigests() = %+v, want only the two addressable rows", refs)
	}
	if refs[0].ReleaseDefinitionID != "def-a" || refs[0].Revision != 1 || refs[0].ValuesDigest != "digest-a" {
		t.Fatalf("refs[0] = %+v, want the rows sorted by definition id with the digest carried through", refs[0])
	}
	if refs[1].ReleaseDefinitionID != "def-b" || refs[1].Revision != 2 || refs[1].ValuesDigest != "digest-b" {
		t.Fatalf("refs[1] = %+v, want the row identity carried through", refs[1])
	}
	// The projection must survive the round trip through the parser cleanup uses.
	recovered := e2e.BaselineRecoveryFromSnapshots(e2e.FixtureSnapshot{
		Identity: e2e.SnapshotIdentity{ReleaseInventories: refs},
	})
	if len(recovered.Revisions) != 2 {
		t.Fatalf("BaselineRecoveryFromSnapshots() revisions = %+v, want the baseline to be usable", recovered.Revisions)
	}
	if got := recovered.Revisions["def-b"]; got.Revision != 2 || got.ValuesDigest != "digest-b" {
		t.Fatalf("recovered release for def-b = %+v, want revision 2 with its content identity", got)
	}
}
