package livewire

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	e2e "github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"
)

// errRejected stands in for a server-side write rejection.
var errRejected = errors.New("operation rejected by the orchestrator")

func TestRevisionReadsInventory(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setInventory(
		inventoryRow("other", 2, nil),
		inventoryRow("e2e-release-target", 7, nil),
	)

	revision, err := h.connector.Revision(context.Background(), "e2e-release-target")
	if err != nil {
		t.Fatalf("Revision() error = %v", err)
	}
	if revision != 7 {
		t.Fatalf("Revision() = %d, want 7", revision)
	}
}

func TestRevisionFailsClosedWhenDefinitionAbsent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setInventory(inventoryRow("other", 3, nil))

	// A definition missing from the inventory must never be reported as
	// revision 0: that would silently become a "baseline" for a rollback.
	if _, err := h.connector.Revision(context.Background(), "e2e-release-target"); err == nil {
		t.Fatal("Revision() error = nil, want a fail-closed error")
	}
}

func TestRevisionRejectsEmptyDefinitionID(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	if _, err := h.connector.Revision(context.Background(), "  "); err == nil {
		t.Fatal("Revision() error = nil, want an error for an empty definition id")
	}
	if got := h.orch.inventoryCallCount(); got != 0 {
		t.Fatalf("inventory calls = %d, want 0 for an empty definition id", got)
	}
}

func TestActiveOperationReportsPresence(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setInventory(inventoryRow("e2e-release-target", 4, &orchestratorv1.ActiveOperationRef{
		OperationId:   "op-live",
		OperationType: "UPGRADE",
		State:         orchestratorv1.OperationStatus_OPERATION_STATUS_RUNNING,
		Actor:         "e2e-runner",
	}))

	active, ok, err := h.connector.ActiveOperation(context.Background(), "e2e-release-target")
	if err != nil {
		t.Fatalf("ActiveOperation() error = %v", err)
	}
	if !ok {
		t.Fatal("ActiveOperation() ok = false, want true")
	}
	if active.ID != "op-live" || active.Type != "UPGRADE" || active.Actor != "e2e-runner" {
		t.Fatalf("ActiveOperation() = %+v", active)
	}
	if active.Status != orchestratorv1.OperationStatus_OPERATION_STATUS_RUNNING.String() {
		t.Fatalf("ActiveOperation() status = %q", active.Status)
	}
}

func TestActiveOperationAbsentIsNotAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setInventory(inventoryRow("e2e-release-target", 4, nil))

	_, ok, err := h.connector.ActiveOperation(context.Background(), "e2e-release-target")
	if err != nil {
		t.Fatalf("ActiveOperation() error = %v", err)
	}
	if ok {
		t.Fatal("ActiveOperation() ok = true, want false when no operation is active")
	}
}

func TestLoginHappensOncePerConnector(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setInventory(inventoryRow("e2e-release-target", 1, nil))

	for range 3 {
		if _, err := h.connector.Revision(context.Background(), "e2e-release-target"); err != nil {
			t.Fatalf("Revision() error = %v", err)
		}
	}
	if got := h.auth.loginCount(); got != 1 {
		t.Fatalf("logins = %d, want exactly 1 for a reused connector", got)
	}
	if h.connector.UserID() != "e2e-runner-id" {
		t.Fatalf("UserID() = %q", h.connector.UserID())
	}
}

func TestUpgradeSendsOptimisticLockAndIdempotencyHeader(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	reference, err := h.connector.Upgrade(context.Background(), stages.UpgradeRequest{
		DefinitionID:     "e2e-release-target",
		BundleID:         "bundle-1",
		ValuesRevisionID: "values-1",
		ExpectedRevision: 4,
	})
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	if reference.ID != "op-created" {
		t.Fatalf("Upgrade() id = %q", reference.ID)
	}
	if got := h.orch.createRequestCount(); got != 1 {
		t.Fatalf("create requests = %d, want 1", got)
	}
	request := h.orch.createRequest(0)
	if request.GetOperationType() != "UPGRADE" {
		t.Fatalf("operation_type = %q, want UPGRADE", request.GetOperationType())
	}
	if request.GetExpectedCurrentRevision() != 4 {
		t.Fatalf("expected_current_revision = %d, want 4", request.GetExpectedCurrentRevision())
	}
	if request.GetBundleId() != "bundle-1" || request.GetValuesRevisionId() != "values-1" {
		t.Fatalf("bundle/values = %q/%q", request.GetBundleId(), request.GetValuesRevisionId())
	}
	// The key must stay stable for a replay from the same revision so the stage
	// dedupes, and carry that revision so a later run whose revision moved on is
	// not rejected as a same-key/different-request conflict.
	if got := h.orch.createKey(0); got != "e2e-upgrade-e2e-release-target-4" {
		t.Fatalf("Idempotency-Key = %q", got)
	}
}

// TestUpgradeIdempotencyKeyTracksTheStartingRevision pins the property a re-run
// depends on. The server rejects a reused key whose request hash differs, and
// each run reads the starting revision fresh, so a key that ignored the revision
// could only ever submit one upgrade per target: the first success moved the
// revision and every later run was refused as a conflict.
func TestUpgradeIdempotencyKeyTracksTheStartingRevision(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	upgrade := func(revision int32) {
		t.Helper()
		if _, err := h.connector.Upgrade(context.Background(), stages.UpgradeRequest{
			DefinitionID:     "e2e-release-target",
			BundleID:         "bundle-1",
			ValuesRevisionID: "values-1",
			ExpectedRevision: revision,
		}); err != nil {
			t.Fatalf("Upgrade(revision %d) error = %v", revision, err)
		}
	}
	upgrade(4)
	upgrade(5)
	if first, second := h.orch.createKey(0), h.orch.createKey(1); first != "e2e-upgrade-e2e-release-target-4" || second != "e2e-upgrade-e2e-release-target-5" {
		t.Fatalf("keys = %q, %q; want a distinct key per starting revision", first, second)
	}
	// A replay from the same revision must still dedupe.
	upgrade(4)
	if replay := h.orch.createKey(2); replay != "e2e-upgrade-e2e-release-target-4" {
		t.Fatalf("replayed key = %q, want the revision-4 key", replay)
	}
}

func TestRollbackCarriesTargetAndExpectedRevision(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setRollbackResponse(&orchestratorv1.RollbackReleaseResponse{OperationId: "op-rb", State: "pending", ToRevision: 4})

	reference, err := h.connector.Rollback(context.Background(), stages.RollbackRequest{
		DefinitionID:     "e2e-release-target",
		TargetRevision:   4,
		ExpectedRevision: 5,
		Reason:           "compensation",
	})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if reference.Revision != 4 {
		t.Fatalf("Rollback() revision = %d, want 4", reference.Revision)
	}
	request := h.orch.rollbackRequest(0)
	if request.GetTargetRevision() != 4 || request.GetExpectedCurrentRevision() != 5 {
		t.Fatalf("target/expected = %d/%d, want 4/5", request.GetTargetRevision(), request.GetExpectedCurrentRevision())
	}
	if request.GetReason() != "compensation" {
		t.Fatalf("reason = %q", request.GetReason())
	}
	if got := h.orch.rollbackKey(0); got != "e2e-rollback-e2e-release-target-5" {
		t.Fatalf("Idempotency-Key = %q", got)
	}
}

func TestAwaitOperationPollsUntilTerminal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.connector.WithPollInterval(time.Millisecond)
	// The first observation is non-terminal and every later one is terminal, so
	// the test proves the adapter waits instead of reading once. Scripting the
	// sequence server-side keeps the assertion free of cross-goroutine state.
	h.orch.setGetSequence(
		runningOperation("op-live", "e2e-release-target"),
		succeededOperation("op-live", "e2e-release-target", 5),
	)

	reference, err := h.connector.AwaitOperation(context.Background(), "op-live")
	if err != nil {
		t.Fatalf("AwaitOperation() error = %v", err)
	}
	if !reference.Succeeded() {
		t.Fatalf("AwaitOperation() = %+v, want succeeded", reference)
	}
	if reference.Revision != 5 {
		t.Fatalf("AwaitOperation() revision = %d, want 5", reference.Revision)
	}
}

func TestAwaitOperationHonorsContextDeadline(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.connector.WithPollInterval(time.Millisecond)
	h.orch.setOperations(map[string]*orchestratorv1.Operation{
		"op-stuck": runningOperation("op-stuck", "e2e-release-target"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := h.connector.AwaitOperation(ctx, "op-stuck"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitOperation() error = %v, want the context deadline", err)
	}
}

func TestAwaitOperationSurfacesReadFailure(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	if _, err := h.connector.AwaitOperation(context.Background(), "op-missing"); err == nil {
		t.Fatal("AwaitOperation() error = nil, want an error for an unobservable operation")
	}
}

func TestCancelRequiresOperationID(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	if err := h.connector.Cancel(context.Background(), " "); err == nil {
		t.Fatal("Cancel() error = nil, want an error for an empty operation id")
	}
	if err := h.connector.Cancel(context.Background(), "op-live"); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	cancels := h.orch.cancelRequests()
	if len(cancels) != 1 || cancels[0] != "op-live" {
		t.Fatalf("cancel calls = %v", cancels)
	}
}

// TestReleaseStageRunsOverConnectAdapters is the integration proof: the real
// stage implementation drives these adapters end to end, so a drift between the
// stage's seam interfaces and the live wiring breaks here and not in CI.
func TestReleaseStageRunsOverConnectAdapters(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setInventory(inventoryRow("e2e-release-target", 4, nil))
	h.orch.setOperations(map[string]*orchestratorv1.Operation{
		"op-created": succeededOperation("op-created", "e2e-release-target", 5),
		"op-rb": {
			OperationId:         "op-rb",
			ReleaseDefinitionId: "e2e-release-target",
			State:               orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED,
			TargetRevision:      4,
		},
	})
	h.orch.setRollbackResponse(&orchestratorv1.RollbackReleaseResponse{OperationId: "op-rb", State: "pending", ToRevision: 4})

	registry := e2e.NewCompensationRegistry()
	stage := stages.NewReleaseStage(h.connector, h.connector, registry, stages.WriteTarget{
		Name:             "release",
		DefinitionID:     "e2e-release-target",
		BundleID:         "bundle-1",
		ValuesRevisionID: "values-1",
	})
	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("ReleaseStage.Run() error = %v", err)
	}
	if stage.BaselineRevision() != 4 {
		t.Fatalf("BaselineRevision() = %d, want 4", stage.BaselineRevision())
	}
	if stage.UpgradedRevision() != 5 {
		t.Fatalf("UpgradedRevision() = %d, want 5", stage.UpgradedRevision())
	}

	// The registered compensation must submit a rollback back to the baseline.
	if err := registry.Run(context.Background()); err != nil {
		t.Fatalf("registry.Run() error = %v", err)
	}
	if got := h.orch.rollbackRequestCount(); got != 1 {
		t.Fatalf("rollback requests = %d, want 1", got)
	}
	if got := h.orch.rollbackRequest(0).GetTargetRevision(); got != 4 {
		t.Fatalf("compensation target = %d, want the baseline 4", got)
	}
	// The lock must come from the terminal operation (5), not from baseline+1.
	if got := h.orch.rollbackRequest(0).GetExpectedCurrentRevision(); got != 5 {
		t.Fatalf("compensation expected = %d, want the reported revision 5", got)
	}
}

func TestUpgradeSurfacesRPCFailure(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.setCreateError(errRejected)
	if _, err := h.connector.Upgrade(context.Background(), stages.UpgradeRequest{DefinitionID: "d"}); err == nil {
		t.Fatal("Upgrade() error = nil, want the RPC failure")
	}
	if !strings.Contains(errRejected.Error(), "rejected") {
		t.Fatalf("unexpected sentinel: %v", errRejected)
	}
}
