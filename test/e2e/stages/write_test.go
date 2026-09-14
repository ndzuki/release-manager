package stages

import (
	"context"
	"errors"
	"testing"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

// operationStatusSucceeded etc. are the wire spellings of the operation
// lifecycle states used by the write-stage fakes.
const (
	wireSucceeded = "OPERATION_STATUS_SUCCEEDED"
	wireFailed    = "OPERATION_STATUS_FAILED"
	wireCancelled = "OPERATION_STATUS_CANCELLED"
	wireQueued    = "OPERATION_STATUS_QUEUED"
)

// revisionDrift makes a definition report a different revision once it has been
// queried at least afterCall times, so a test can simulate a non-target that
// moved between two observations.
type revisionDrift struct {
	afterCall int
	value     int32
}

// writeFake is a deterministic ReleaseObserver + OperationWriter + operation
// driver. Every call is recorded so tests assert the formal request shape
// rather than only the returned error.
type writeFake struct {
	revision    int32
	revisionErr error
	// revisions overrides the default revision per definition id.
	revisions map[string]int32
	// revisionDrift overrides a definition's revision from the Nth query on.
	revisionDrift map[string]revisionDrift
	revisionCalls map[string]int

	active    ActiveOperation
	hasActive bool
	activeErr error

	upgradeRef OperationRef
	upgradeErr error
	upgrades   []UpgradeRequest

	rollbackRef OperationRef
	rollbackErr error
	rollbacks   []RollbackRequest

	cancels   []string
	cancelErr error

	awaitErr map[string]error
	await    map[string]OperationRef
}

func newWriteFake() *writeFake {
	return &writeFake{
		revision: 3,
		upgradeRef: OperationRef{
			ID: "op-upgrade", DefinitionID: "def-release", Type: "UPGRADE",
		},
		rollbackRef: OperationRef{
			ID: "op-rollback", DefinitionID: "def-release", Type: "ROLLBACK",
		},
		revisions:     map[string]int32{},
		revisionDrift: map[string]revisionDrift{},
		revisionCalls: map[string]int{},
		awaitErr:      map[string]error{},
		await:         map[string]OperationRef{},
	}
}

func (f *writeFake) Revision(_ context.Context, definitionID string) (int32, error) {
	if f.revisionErr != nil {
		return 0, f.revisionErr
	}
	f.revisionCalls[definitionID]++
	if drift, ok := f.revisionDrift[definitionID]; ok && f.revisionCalls[definitionID] >= drift.afterCall {
		return drift.value, nil
	}
	if revision, ok := f.revisions[definitionID]; ok {
		return revision, nil
	}
	return f.revision, nil
}

func (f *writeFake) ActiveOperation(context.Context, string) (ActiveOperation, bool, error) {
	return f.active, f.hasActive, f.activeErr
}

func (f *writeFake) Upgrade(_ context.Context, req UpgradeRequest) (OperationRef, error) {
	f.upgrades = append(f.upgrades, req)
	if f.upgradeErr != nil {
		return OperationRef{}, f.upgradeErr
	}
	return f.upgradeRef, nil
}

func (f *writeFake) Rollback(_ context.Context, req RollbackRequest) (OperationRef, error) {
	f.rollbacks = append(f.rollbacks, req)
	if f.rollbackErr != nil {
		return OperationRef{}, f.rollbackErr
	}
	return f.rollbackRef, nil
}

func (f *writeFake) Cancel(_ context.Context, operationID string) error {
	f.cancels = append(f.cancels, operationID)
	return f.cancelErr
}

func (f *writeFake) AwaitOperation(_ context.Context, operationID string) (OperationRef, error) {
	if err, ok := f.awaitErr[operationID]; ok {
		return OperationRef{ID: operationID}, err
	}
	if ref, ok := f.await[operationID]; ok {
		return ref, nil
	}
	return OperationRef{ID: operationID, Status: wireSucceeded}, nil
}

func releaseTarget() WriteTarget {
	return WriteTarget{
		Name:             "e2e-release-target",
		DefinitionID:     "def-release",
		BundleID:         "bundle-1",
		ValuesRevisionID: "values-1",
	}
}

func TestNormalizeOperationStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "OPERATION_STATUS_SUCCEEDED", want: "succeeded"},
		{in: "succeeded", want: "succeeded"},
		{in: "STATUS_RUNNING", want: "running"},
		{in: "  Operation_Status_Failed ", want: "failed"},
		{in: "", want: ""},
	}

	for _, test := range tests {
		if got := normalizeOperationStatus(test.in); got != test.want {
			t.Fatalf("normalizeOperationStatus(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestOperationRefTerminalAndSucceeded(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"OPERATION_STATUS_SUCCEEDED", "OPERATION_STATUS_FAILED", "OPERATION_STATUS_CANCELLED", "OPERATION_STATUS_TIMEOUT"} {
		if !(OperationRef{Status: status}).Terminal() {
			t.Fatalf("%s must be terminal", status)
		}
	}
	for _, status := range []string{"OPERATION_STATUS_QUEUED", "OPERATION_STATUS_RUNNING", "OPERATION_STATUS_PREFLIGHT", ""} {
		if (OperationRef{Status: status}).Terminal() {
			t.Fatalf("%s must not be terminal", status)
		}
	}
	if !(OperationRef{Status: "OPERATION_STATUS_SUCCEEDED"}).Succeeded() {
		t.Fatal("succeeded status must report Succeeded()")
	}
	if (OperationRef{Status: "OPERATION_STATUS_CANCELLED"}).Succeeded() {
		t.Fatal("cancelled status must not report Succeeded()")
	}
}

func TestWriteTargetValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target WriteTarget
		want   error
	}{
		{name: "complete", target: releaseTarget()},
		{
			name:   "definition missing",
			target: WriteTarget{BundleID: "b", ValuesRevisionID: "v"},
			want:   ErrSnapshotNotFound,
		},
		{
			name:   "bundle missing",
			target: WriteTarget{DefinitionID: "d", ValuesRevisionID: "v"},
			want:   ErrSnapshotNotFound,
		},
		{
			name:   "values missing",
			target: WriteTarget{DefinitionID: "d", BundleID: "b"},
			want:   ErrSnapshotNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.target.validateUpgrade("release")
			if test.want == nil {
				if err != nil {
					t.Fatalf("validateUpgrade() error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("validateUpgrade() error = %v, want errors.Is(..., %v)", err, test.want)
			}
		})
	}
}

func TestTakeoverRejectsForeignActor(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	fake.hasActive = true
	fake.active = ActiveOperation{ID: "op-foreign", Actor: "someone-else", Status: wireQueued}

	err := takeover(context.Background(), "release", "def-release", fake, fake)
	if !errors.Is(err, ErrReleaseBusy) {
		t.Fatalf("takeover() error = %v, want ErrReleaseBusy", err)
	}
	if len(fake.cancels) != 0 {
		t.Fatalf("foreign operation must never be cancelled, got %v", fake.cancels)
	}
}

func TestTakeoverCancelsRunnerResidual(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	fake.hasActive = true
	fake.active = ActiveOperation{ID: "op-residual", Actor: RunnerActor, Status: wireQueued}
	fake.await["op-residual"] = OperationRef{ID: "op-residual", Status: wireCancelled}

	if err := takeover(context.Background(), "release", "def-release", fake, fake); err != nil {
		t.Fatalf("takeover() error = %v", err)
	}
	if len(fake.cancels) != 1 || fake.cancels[0] != "op-residual" {
		t.Fatalf("cancels = %v, want [op-residual]", fake.cancels)
	}
}

func TestTakeoverTimeoutIsCleanupTimeout(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	fake.hasActive = true
	fake.active = ActiveOperation{ID: "op-residual", Actor: RunnerActor, Status: wireQueued}
	fake.cancelErr = errors.New("private transport detail")

	err := takeover(context.Background(), "release", "def-release", fake, fake)
	if !errors.Is(err, ErrCleanupTimeout) {
		t.Fatalf("takeover() error = %v, want ErrCleanupTimeout", err)
	}
}

func TestRegistryHappyPathCompensation(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	registry := e2e.NewCompensationRegistry()
	target := releaseTarget()

	result, baseline, err := runUpgrade(context.Background(), "release", fake, fake, registry, target, CompensationReleaseRollback, nil)
	if err != nil {
		t.Fatalf("runUpgrade() error = %v", err)
	}
	if !result.Succeeded() {
		t.Fatalf("result = %#v, want succeeded", result)
	}
	if baseline != 3 {
		t.Fatalf("baseline = %d, want 3", baseline)
	}
	if len(fake.upgrades) != 1 {
		t.Fatalf("upgrades = %d, want 1", len(fake.upgrades))
	}
	if got := fake.upgrades[0].ExpectedRevision; got != 3 {
		t.Fatalf("ExpectedRevision = %d, want the live observed 3", got)
	}
	if registry.Len() != 1 {
		t.Fatalf("registry.Len() = %d, want exactly one write-stage compensation", registry.Len())
	}

	if err := registry.Run(context.Background()); err != nil {
		t.Fatalf("registry.Run() error = %v", err)
	}
	if len(fake.rollbacks) != 1 {
		t.Fatalf("rollbacks = %d, want 1", len(fake.rollbacks))
	}
	restore := fake.rollbacks[0]
	if restore.TargetRevision != 3 || restore.ExpectedRevision != 4 {
		t.Fatalf("restore = %#v, want target 3 expected 4", restore)
	}
	if restore.DefinitionID != "def-release" {
		t.Fatalf("restore definition = %q, want def-release", restore.DefinitionID)
	}
}
