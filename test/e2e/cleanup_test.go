package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// errTestObservation stands in for a workload the outcome check cannot read.
var errTestObservation = errors.New("observation unavailable")

type recoveryFake struct {
	rows        []CleanupRow
	cancelled   []string
	rolled      []string
	replicas    []string
	cancelErr   error
	rollErr     error
	replicaErr  error
	digests     []ReleaseDigest
	digestErr   error
	digestReads int
}

func (f *recoveryFake) ListReleaseInventory(context.Context) ([]CleanupRow, error) {
	return f.rows, nil
}

// ListReleaseDigests answers with the content identities the rollback decision
// and the revision check compare. Counting the reads lets a test pin that the
// digest is read once per pass rather than once per row.
func (f *recoveryFake) ListReleaseDigests(context.Context) ([]ReleaseDigest, error) {
	f.digestReads++
	if f.digestErr != nil {
		return nil, f.digestErr
	}
	return f.digests, nil
}

func (f *recoveryFake) CancelOperation(_ context.Context, operationID, _ string) error {
	if f.cancelErr != nil {
		return f.cancelErr
	}
	f.cancelled = append(f.cancelled, operationID)
	return nil
}

func (f *recoveryFake) RollbackRelease(_ context.Context, definitionID string, _, _ int32, _ string) error {
	if f.rollErr != nil {
		return f.rollErr
	}
	f.rolled = append(f.rolled, definitionID)
	return nil
}

func (f *recoveryFake) SetReplicas(_ context.Context, definitionID, workloadRef string, replicas int32, _ string) error {
	if f.replicaErr != nil {
		return f.replicaErr
	}
	f.replicas = append(f.replicas, fmt.Sprintf("%s|%s|%d", definitionID, workloadRef, replicas))
	return nil
}

func runnerRow(definitionID string, revision int32, active *CleanupOperation) CleanupRow {
	return CleanupRow{
		CustomerID:   "dev-customer-a",
		ClusterID:    "dev-customer-a-direct",
		DefinitionID: definitionID,
		Namespace:    "e2e-release",
		ReleaseName:  "e2e-release",
		Revision:     revision,
		Active:       active,
	}
}

func nonTerminal(op, actor string) *CleanupOperation {
	return &CleanupOperation{OperationID: op, OperationType: "UPGRADE", State: "OPERATION_STATUS_RUNNING", Actor: actor}
}

func TestRunCleanupCancelsRunnerOwnedResiduals(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	fake := &recoveryFake{rows: []CleanupRow{
		runnerRow("e2e-release-target", 1, nonTerminal("op-runner", "54013831-4a47-40ce-80a9-df090905ed56")),
		runnerRow("e2e-isolation-target", 1, nonTerminal("op-name", RunnerUsername)),
		runnerRow("e2e-emergency-target", 1, nonTerminal("op-other", "some-other-user")),
	}}
	report, err := RunCleanup(context.Background(), &BaselineRecovery{}, "54013831-4a47-40ce-80a9-df090905ed56", fake, logger)
	if err != nil {
		t.Fatalf("RunCleanup() error = %v", err)
	}
	if len(fake.cancelled) != 2 {
		t.Fatalf("cancelled = %v, want 2 runner-owned operations", fake.cancelled)
	}
	if report.CancelledOperationIDs[0] != "op-name" || report.CancelledOperationIDs[1] != "op-runner" {
		t.Fatalf("cancelled report = %v, want deterministic [op-name op-runner]", report.CancelledOperationIDs)
	}
	if len(report.ResidualNonTerminal) != 0 {
		t.Fatalf("residual = %v, want none", report.ResidualNonTerminal)
	}
	if !report.SkippedReplicasRestore {
		t.Fatal("SkippedReplicasRestore = false, want true (baseline carries no replicas yet)")
	}
	if !strings.Contains(logs.String(), "replicas restore skipped") {
		t.Fatalf("logs = %q, want replicas degradation warning", logs.String())
	}
}

// TestRunCleanupRestoresBaselineReplicas covers the collected-baseline path:
// with replicas recorded, cleanup re-applies each one through the formal
// emergency API instead of degrading to a warning (AC-066-23/34).
func TestRunCleanupRestoresBaselineReplicas(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	fake := &recoveryFake{}
	baseline := &BaselineRecovery{
		Revisions: map[string]BaselineRelease{},
		Replicas: []WorkloadReplicaRef{
			{ReleaseDefinitionID: "def-emergency", WorkloadRef: "deployments/e2e-emergency/e2e-emergency", Replicas: 3},
			{ReleaseDefinitionID: "def-release", WorkloadRef: "deployments/e2e-release/e2e-release", Replicas: 1},
		},
	}
	report, err := RunCleanup(context.Background(), baseline, "runner-1", fake, logger)
	if err != nil {
		t.Fatalf("RunCleanup() error = %v", err)
	}
	want := []string{
		"def-emergency|deployments/e2e-emergency/e2e-emergency|3",
		"def-release|deployments/e2e-release/e2e-release|1",
	}
	if len(fake.replicas) != len(want) {
		t.Fatalf("set replicas calls = %v, want %v", fake.replicas, want)
	}
	for index := range want {
		if fake.replicas[index] != want[index] {
			t.Fatalf("set replicas[%d] = %q, want %q", index, fake.replicas[index], want[index])
		}
	}
	if report.SkippedReplicasRestore {
		t.Fatal("SkippedReplicasRestore = true, want false when the baseline records replicas")
	}
	if len(report.RestoredReplicas) != 2 {
		t.Fatalf("RestoredReplicas = %v, want both workloads", report.RestoredReplicas)
	}
	if len(report.ResidualNonTerminal) != 0 {
		t.Fatalf("residual = %v, want none", report.ResidualNonTerminal)
	}
}

// TestRunCleanupReportsFailedReplicaRestore proves a rejected restore is
// reported as residual rather than counted as restored.
func TestRunCleanupReportsFailedReplicaRestore(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	fake := &recoveryFake{replicaErr: context.DeadlineExceeded}
	baseline := &BaselineRecovery{
		Revisions: map[string]BaselineRelease{},
		Replicas: []WorkloadReplicaRef{
			{ReleaseDefinitionID: "def-emergency", WorkloadRef: "deployments/e2e-emergency/e2e-emergency", Replicas: 3},
		},
	}
	report, err := RunCleanup(context.Background(), baseline, "runner-1", fake, logger)
	if err != nil {
		t.Fatalf("RunCleanup() error = %v", err)
	}
	if len(report.RestoredReplicas) != 0 {
		t.Fatalf("RestoredReplicas = %v, want none after a failed restore", report.RestoredReplicas)
	}
	if len(report.ResidualNonTerminal) != 1 || report.ResidualNonTerminal[0] != "deployments/e2e-emergency/e2e-emergency" {
		t.Fatalf("residual = %v, want the failed workload", report.ResidualNonTerminal)
	}
	if report.SkippedReplicasRestore {
		t.Fatal("SkippedReplicasRestore = true, want false: replicas were recorded, the write failed")
	}
}

// TestBaselineRecoveryFromSnapshotsDerivesReplicas covers the projection from
// baseline.json: addressable rows survive, incomplete ones are dropped rather
// than guessed, and the result is deterministic.
func TestBaselineRecoveryFromSnapshotsDerivesReplicas(t *testing.T) {
	t.Parallel()

	snapshot := FixtureSnapshot{Identity: SnapshotIdentity{
		WorkloadReplicas: []WorkloadReplicaRef{
			{ReleaseDefinitionID: "", WorkloadRef: "deployments/x/x", Replicas: 2},
			{ReleaseDefinitionID: "def-b", WorkloadRef: "", Replicas: 2},
			{ReleaseDefinitionID: "def-c", WorkloadRef: "deployments/c/c", Replicas: -1},
			{ReleaseDefinitionID: "def-b", WorkloadRef: "deployments/b/b", Replicas: 2},
			{ReleaseDefinitionID: "def-a", WorkloadRef: "deployments/a/a", Replicas: 0},
		},
	}}
	recovery := BaselineRecoveryFromSnapshots(snapshot)
	if len(recovery.Replicas) != 2 {
		t.Fatalf("replicas = %+v, want only the two addressable rows", recovery.Replicas)
	}
	if recovery.Replicas[0].ReleaseDefinitionID != "def-a" || recovery.Replicas[1].ReleaseDefinitionID != "def-b" {
		t.Fatalf("replicas = %+v, want deterministic [def-a def-b] ordering", recovery.Replicas)
	}
	// A zero replica count is a real baseline (a scaled-to-zero workload), so
	// it must survive the projection.
	if recovery.Replicas[0].Replicas != 0 {
		t.Fatalf("def-a replicas = %d, want 0 preserved", recovery.Replicas[0].Replicas)
	}
}

func TestRunCleanupDoesNotCancelNonRunnerOrTerminal(t *testing.T) {
	t.Parallel()

	fake := &recoveryFake{rows: []CleanupRow{
		runnerRow("e2e-release-target", 1, nonTerminal("op-other", "someone-else")),
		runnerRow("e2e-isolation-target", 1, &CleanupOperation{OperationID: "op-terminal", Actor: RunnerUsername, State: "OPERATION_STATUS_SUCCEEDED"}),
	}}
	report, err := RunCleanup(context.Background(), &BaselineRecovery{}, "runner-id", fake, slog.Default())
	if err != nil {
		t.Fatalf("RunCleanup() error = %v", err)
	}
	if len(fake.cancelled) != 0 {
		t.Fatalf("cancelled = %v, want none", fake.cancelled)
	}
	if report.NonZero() && (len(report.CancelledOperationIDs) != 0 || len(report.ResidualNonTerminal) != 0) {
		t.Fatalf("report = %+v, want no cancellation activity", report)
	}
}

func TestRunCleanupRollsBackRevisionDrift(t *testing.T) {
	t.Parallel()

	baseline := &BaselineRecovery{Revisions: map[string]BaselineRelease{"e2e-release-target": {Revision: 1}}}
	fake := &recoveryFake{rows: []CleanupRow{
		runnerRow("e2e-release-target", 2, nil),   // drifted to revision 2
		runnerRow("e2e-isolation-target", 1, nil), // already at baseline (no target entry -> skip)
	}}
	report, err := RunCleanup(context.Background(), baseline, "runner-id", fake, slog.Default())
	if err != nil {
		t.Fatalf("RunCleanup() error = %v", err)
	}
	if len(fake.rolled) != 1 || fake.rolled[0] != "e2e-release-target" {
		t.Fatalf("rolled = %v, want [e2e-release-target]", fake.rolled)
	}
	if len(report.RolledBackDefinitions) != 1 {
		t.Fatalf("rolled report = %v, want one", report.RolledBackDefinitions)
	}
}

func TestRunCleanupSkipsRevisionRestoreWhenBaselineMissing(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	// No baseline at all -> residual-only degradation.
	fake := &recoveryFake{rows: []CleanupRow{
		runnerRow("e2e-release-target", 2, nil),
	}}
	report, err := RunCleanup(context.Background(), nil, "runner-id", fake, logger)
	if err != nil {
		t.Fatalf("RunCleanup() error = %v", err)
	}
	if !report.BaselineMissing {
		t.Fatal("BaselineMissing = false, want true")
	}
	if len(fake.rolled) != 0 {
		t.Fatalf("rolled = %v, want none without a baseline", fake.rolled)
	}
	if len(report.SkippedRevisionRestore) != 1 || report.SkippedRevisionRestore[0] != "e2e-release-target" {
		t.Fatalf("skipped restore = %v, want [e2e-release-target]", report.SkippedRevisionRestore)
	}
	if !strings.Contains(logs.String(), "baseline.json missing") {
		t.Fatalf("logs = %q, want residual-only warning", logs.String())
	}
}

func TestRunCleanupReportsCancelAndRollbackFailures(t *testing.T) {
	t.Parallel()

	fake := &recoveryFake{
		rows: []CleanupRow{
			runnerRow("e2e-release-target", 1, nonTerminal("op-bad", RunnerUsername)),
			runnerRow("e2e-isolation-target", 2, nil),
		},
		cancelErr: context.DeadlineExceeded,
		rollErr:   context.DeadlineExceeded,
	}
	baseline := &BaselineRecovery{Revisions: map[string]BaselineRelease{"e2e-isolation-target": {Revision: 1}}}
	report, err := RunCleanup(context.Background(), baseline, "runner-id", fake, slog.Default())
	if err != nil {
		t.Fatalf("RunCleanup() error = %v", err)
	}
	if len(fake.cancelled) != 0 || len(fake.rolled) != 0 {
		t.Fatalf("unexpected successful recovery calls: cancelled=%v rolled=%v", fake.cancelled, fake.rolled)
	}
	if len(report.ResidualNonTerminal) != 2 {
		t.Fatalf("residual = %v, want both failures recorded", report.ResidualNonTerminal)
	}
}

func TestBaselineRecoveryFromSnapshotsDerivesRevisions(t *testing.T) {
	t.Parallel()

	snapshot := FixtureSnapshot{}
	snapshot.Identity.ReleaseInventories = []InventoryRef{
		{ReleaseDefinitionID: "e2e-release-target", Revision: 2, ValuesDigest: "digest-2"},
		{ReleaseDefinitionID: "e2e-emergency-target", Revision: 0},
		{ReleaseDefinitionID: "", Revision: 3},
	}
	baseline := BaselineRecoveryFromSnapshots(snapshot)
	recorded, ok := baseline.Revisions["e2e-release-target"]
	if len(baseline.Revisions) != 1 || !ok || recorded.Revision != 2 {
		t.Fatalf("baseline revisions = %v, want only e2e-release-target:2", baseline.Revisions)
	}
	if recorded.ValuesDigest != "digest-2" {
		t.Fatalf("baseline digest = %q, want the content identity carried through so cleanup can compare content rather than a revision number", recorded.ValuesDigest)
	}
}

func TestCleanupDeadlineBoundsContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := CleanupDeadline(context.Background(), 0)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("cleanup context expired immediately")
	default:
	}
}

// TestCleanupDeadlineSpansTheRunInducedAgentReconnect pins the property the
// default exists for. The run's restart stage restarts the orchestrator and drops
// every operator command stream, and the agents reconnect on their own backoff
// (~32s in the real smoke) before any restore can be applied. A budget that
// expires first makes every recovery write fail regardless of retries: cleanup
// then reports residuals it could not have avoided.
func TestCleanupDeadlineSpansTheRunInducedAgentReconnect(t *testing.T) {
	t.Parallel()

	// The smallest budget that must be exceeded: the observed reconnect delay
	// plus one emergency operation apply window (the orchestrator's own
	// operation timeout is 30s).
	const mustExceed = 62 * time.Second

	ctx, cancel := CleanupDeadline(context.Background(), 0)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("CleanupDeadline() carries no deadline")
	}
	if budget := time.Until(deadline); budget <= mustExceed {
		t.Fatalf("cleanup budget = %s, want more than %s so a restore can outlast the agent reconnect", budget, mustExceed)
	}
}

// replicaObserverFake records what the outcome check read and answers with a
// count per workload reference.
type replicaObserverFake struct {
	counts  map[string]int32
	readErr error
	reads   []string
}

func (f *replicaObserverFake) ObserveReplicas(_ context.Context, cluster, namespace, workloadName string) (int32, error) {
	key := cluster + "/" + namespace + "/" + workloadName
	f.reads = append(f.reads, key)
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.counts[key], nil
}

// TestVerifyRestoreReportsAWorkloadThatNeverReturnedToTheBaseline covers the
// hole this phase exists for: a replica restore whose write and operation both
// succeeded while the workload kept the count the run had set. Action
// verification cannot see that; only the read back can.
func TestVerifyRestoreReportsAWorkloadThatNeverReturnedToTheBaseline(t *testing.T) {
	t.Parallel()

	baseline := &BaselineRecovery{Replicas: []WorkloadReplicaRef{{
		ReleaseDefinitionID: "def-1",
		Cluster:             "dev-customer-a-direct",
		WorkloadRef:         "deployments/e2e-emergency/release-fixture",
		Replicas:            1,
	}}}
	observer := &replicaObserverFake{counts: map[string]int32{
		"dev-customer-a-direct/e2e-emergency/release-fixture": 2,
	}}

	report := VerifyRestore(context.Background(), baseline, nil, observer, slog.Default())
	if len(report.ResidualReplicas) != 1 {
		t.Fatalf("ResidualReplicas = %v, want the workload reported as still changed", report.ResidualReplicas)
	}
	if len(report.UnverifiedReplicas) != 0 {
		t.Fatalf("UnverifiedReplicas = %v, want a successful read to count as verified", report.UnverifiedReplicas)
	}
	if !report.NonZero() {
		t.Fatal("NonZero() = false, want a residual to make the report non-empty")
	}
}

// TestVerifyRestoreAcceptsAWorkloadBackAtTheBaseline pins the other direction, so
// the check cannot be satisfied by reporting a residual unconditionally.
func TestVerifyRestoreAcceptsAWorkloadBackAtTheBaseline(t *testing.T) {
	t.Parallel()

	baseline := &BaselineRecovery{Replicas: []WorkloadReplicaRef{{
		Cluster:     "dev-customer-a-direct",
		WorkloadRef: "deployments/e2e-emergency/release-fixture",
		Replicas:    1,
	}}}
	observer := &replicaObserverFake{counts: map[string]int32{
		"dev-customer-a-direct/e2e-emergency/release-fixture": 1,
	}}

	report := VerifyRestore(context.Background(), baseline, nil, observer, slog.Default())
	if len(report.ResidualReplicas) != 0 || len(report.UnverifiedReplicas) != 0 {
		t.Fatalf("report = %+v, want a workload at the baseline count to be neither residual nor unverified", report)
	}
	if report.NonZero() {
		t.Fatalf("NonZero() = true for a clean verification: %+v", report)
	}
	if len(observer.reads) != 1 {
		t.Fatalf("reads = %v, want the workload read once", observer.reads)
	}
}

// TestVerifyRestoreNeverMistakesUnreadForRestored covers the honesty rule. A
// missing observer, an unreadable workload, and a baseline row with no cluster
// are all "not verified" rather than "matched": reporting a silent pass would
// recreate the false success this phase replaced.
func TestVerifyRestoreNeverMistakesUnreadForRestored(t *testing.T) {
	t.Parallel()

	rows := []WorkloadReplicaRef{
		{Cluster: "c", WorkloadRef: "deployments/ns/one", Replicas: 1},
		{WorkloadRef: "deployments/ns/two", Replicas: 1},
		{Cluster: "c", WorkloadRef: "not-a-workload-ref", Replicas: 1},
	}
	baseline := &BaselineRecovery{Replicas: rows}

	report := VerifyRestore(context.Background(), baseline, nil, nil, slog.Default())
	if len(report.UnverifiedReplicas) != len(rows) {
		t.Fatalf("UnverifiedReplicas = %v, want all %d rows reported when no observer is wired", report.UnverifiedReplicas, len(rows))
	}
	if len(report.ResidualReplicas) != 0 {
		t.Fatalf("ResidualReplicas = %v, want an unread workload never reported as a mismatch", report.ResidualReplicas)
	}

	failing := &replicaObserverFake{readErr: errTestObservation}
	report = VerifyRestore(context.Background(), baseline, nil, failing, slog.Default())
	if len(report.UnverifiedReplicas) != len(rows) {
		t.Fatalf("UnverifiedReplicas = %v, want every unreadable row reported", report.UnverifiedReplicas)
	}
}

// TestMergeVerificationKeepsOneReport pins that the two phases produce a single
// account: a residual found by the outcome check must not appear in a separate
// artifact a reader could miss.
func TestMergeVerificationKeepsOneReport(t *testing.T) {
	t.Parallel()

	report := CleanupReport{RestoredReplicas: []string{"deployments/ns/one"}}
	report.MergeVerification(CleanupReport{
		ResidualReplicas:   []string{"deployments/ns/two"},
		UnverifiedReplicas: []string{"deployments/ns/three"},
	})
	if len(report.RestoredReplicas) != 1 || len(report.ResidualReplicas) != 1 || len(report.UnverifiedReplicas) != 1 {
		t.Fatalf("merged report = %+v, want the recovery and verification fields together", report)
	}
}

// TestRunCleanupLeavesAReleaseWhoseContentIsTheBaseline covers the defect that
// made cleanup un-convergent.
//
// RollbackRelease advances the revision counter rather than restoring the number:
// a rollback to revision 21 read back as 23, and as 24 after the next run. The
// trigger compared that number, so it rolled back again on every invocation
// forever and inflated the counter each time -- which also made "cleanup is
// idempotent" false, and an automatic pre-run cleanup unsafe to build on. The
// content is what a rollback restores, so the content is what decides.
func TestRunCleanupLeavesAReleaseWhoseContentIsTheBaseline(t *testing.T) {
	t.Parallel()

	baseline := &BaselineRecovery{Revisions: map[string]BaselineRelease{
		"e2e-release-target": {Revision: 21, ValuesDigest: "digest-baseline"},
	}}
	// The revision differs from the baseline while the content does not: exactly
	// the state a completed rollback leaves behind.
	fake := &recoveryFake{
		rows:    []CleanupRow{runnerRow("e2e-release-target", 23, nil)},
		digests: []ReleaseDigest{{ReleaseDefinitionID: "e2e-release-target", Revision: 23, ValuesDigest: "digest-baseline"}},
	}
	report, err := RunCleanup(context.Background(), baseline, "runner-id", fake, slog.Default())
	if err != nil {
		t.Fatalf("RunCleanup() error = %v", err)
	}
	if len(fake.rolled) != 0 {
		t.Fatalf("rolled back %v, want no rollback when the content is already the baseline", fake.rolled)
	}
	if len(report.RolledBackDefinitions) != 0 || len(report.SkippedRevisionRestore) != 0 {
		t.Fatalf("report = %+v, want a converged pass to report nothing to restore", report)
	}
}

// TestRunCleanupRollsBackAReleaseWhoseContentDrifted is the other half: the
// content comparison must still catch a release the run moved.
func TestRunCleanupRollsBackAReleaseWhoseContentDrifted(t *testing.T) {
	t.Parallel()

	baseline := &BaselineRecovery{Revisions: map[string]BaselineRelease{
		"e2e-release-target": {Revision: 21, ValuesDigest: "digest-baseline"},
	}}
	fake := &recoveryFake{
		rows:    []CleanupRow{runnerRow("e2e-release-target", 22, nil)},
		digests: []ReleaseDigest{{ReleaseDefinitionID: "e2e-release-target", Revision: 22, ValuesDigest: "digest-run"}},
	}
	report, err := RunCleanup(context.Background(), baseline, "runner-id", fake, slog.Default())
	if err != nil {
		t.Fatalf("RunCleanup() error = %v", err)
	}
	if len(fake.rolled) != 1 || len(report.RolledBackDefinitions) != 1 {
		t.Fatalf("rolled=%v report=%+v, want the drifted release rolled back", fake.rolled, report)
	}
	if fake.digestReads != 1 {
		t.Fatalf("digest reads = %d, want one read shared by every row", fake.digestReads)
	}
}

// TestAlreadyAtBaselineDegradesToTheRevisionWithoutADigest pins the documented
// fallback: a baseline written before rows carried a content identity, or a digest
// read that failed, behaves as it always did instead of reporting a false match.
func TestAlreadyAtBaselineDegradesToTheRevisionWithoutADigest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		baseline   BaselineRelease
		revision   int32
		digest     string
		wantAtBase bool
	}{
		{
			name:       "content identity matches",
			baseline:   BaselineRelease{Revision: 21, ValuesDigest: "same"},
			revision:   24,
			digest:     "same",
			wantAtBase: true,
		},
		{
			name:       "content identity differs",
			baseline:   BaselineRelease{Revision: 21, ValuesDigest: "baseline"},
			revision:   21,
			digest:     "run",
			wantAtBase: false,
		},
		{
			name:       "no baseline digest falls back to the revision",
			baseline:   BaselineRelease{Revision: 21},
			revision:   21,
			digest:     "run",
			wantAtBase: true,
		},
		{
			name:       "unread digest falls back to the revision",
			baseline:   BaselineRelease{Revision: 21, ValuesDigest: "baseline"},
			revision:   21,
			digest:     "",
			wantAtBase: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := alreadyAtBaseline(testCase.baseline, testCase.revision, testCase.digest); got != testCase.wantAtBase {
				t.Fatalf("alreadyAtBaseline() = %v, want %v", got, testCase.wantAtBase)
			}
		})
	}
}

// TestVerifyRestoreChecksReleaseContentNotRevisionNumbers covers the revision half
// of the outcome check. Comparing revision numbers would report every release a
// run moved as a permanent mismatch, so the check compares content.
func TestVerifyRestoreChecksReleaseContentNotRevisionNumbers(t *testing.T) {
	t.Parallel()

	baseline := &BaselineRecovery{
		Revisions: map[string]BaselineRelease{
			"def-restored": {Revision: 21, ValuesDigest: "digest-baseline"},
			"def-drifted":  {Revision: 21, ValuesDigest: "digest-baseline"},
			"def-legacy":   {Revision: 21},
		},
	}
	recovery := &recoveryFake{digests: []ReleaseDigest{
		// A completed rollback: the number moved on, the content came back.
		{ReleaseDefinitionID: "def-restored", Revision: 24, ValuesDigest: "digest-baseline"},
		{ReleaseDefinitionID: "def-drifted", Revision: 22, ValuesDigest: "digest-run"},
		{ReleaseDefinitionID: "def-legacy", Revision: 21, ValuesDigest: "digest-baseline"},
	}}
	report := VerifyRestore(context.Background(), baseline, recovery, nil, slog.Default())

	if len(report.ResidualRevisions) != 1 || report.ResidualRevisions[0] != "def-drifted" {
		t.Fatalf("ResidualRevisions = %v, want only the drifted definition", report.ResidualRevisions)
	}
	if len(report.UnverifiedRevisions) != 1 || report.UnverifiedRevisions[0] != "def-legacy" {
		t.Fatalf("UnverifiedRevisions = %v, want the digest-less baseline row reported rather than assumed", report.UnverifiedRevisions)
	}
}

// TestVerifyRestoreNeverMistakesAnUnreadRevisionForRestored keeps the honesty rule
// on the revision half too.
func TestVerifyRestoreNeverMistakesAnUnreadRevisionForRestored(t *testing.T) {
	t.Parallel()

	baseline := &BaselineRecovery{Revisions: map[string]BaselineRelease{
		"def-1": {Revision: 1, ValuesDigest: "digest-1"},
		"def-2": {Revision: 2, ValuesDigest: "digest-2"},
	}}

	report := VerifyRestore(context.Background(), baseline, &recoveryFake{digestErr: errTestObservation}, nil, slog.Default())
	if len(report.UnverifiedRevisions) != 2 || len(report.ResidualRevisions) != 0 {
		t.Fatalf("report = %+v, want a failed read reported as unverified rather than as a mismatch or a match", report)
	}

	// A digest the server did not report for a definition is also unverified.
	report = VerifyRestore(context.Background(), baseline, &recoveryFake{digests: []ReleaseDigest{
		{ReleaseDefinitionID: "def-1", Revision: 1, ValuesDigest: "digest-1"},
	}}, nil, slog.Default())
	if len(report.UnverifiedRevisions) != 1 || report.UnverifiedRevisions[0] != "def-2" {
		t.Fatalf("UnverifiedRevisions = %v, want the definition the read did not cover", report.UnverifiedRevisions)
	}
}
