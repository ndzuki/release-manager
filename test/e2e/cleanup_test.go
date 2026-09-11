package e2e

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

type recoveryFake struct {
	rows       []CleanupRow
	cancelled  []string
	rolled     []string
	replicas   []string
	cancelErr  error
	rollErr    error
	replicaErr error
}

func (f *recoveryFake) ListReleaseInventory(context.Context) ([]CleanupRow, error) {
	return f.rows, nil
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
		Revisions: map[string]int32{},
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
		Revisions: map[string]int32{},
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

	baseline := &BaselineRecovery{Revisions: map[string]int32{"e2e-release-target": 1}}
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
	baseline := &BaselineRecovery{Revisions: map[string]int32{"e2e-isolation-target": 1}}
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
		{ReleaseDefinitionID: "e2e-release-target", Revision: 2},
		{ReleaseDefinitionID: "e2e-emergency-target", Revision: 0},
		{ReleaseDefinitionID: "", Revision: 3},
	}
	baseline := BaselineRecoveryFromSnapshots(snapshot)
	if len(baseline.Revisions) != 1 || baseline.Revisions["e2e-release-target"] != 2 {
		t.Fatalf("baseline revisions = %v, want only e2e-release-target:2", baseline.Revisions)
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
