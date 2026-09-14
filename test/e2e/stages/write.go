package stages

import (
	"context"
	"fmt"
	"strings"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

// Write-stage root causes. These join the read-only codes declared in
// control_plane.go and are surfaced verbatim in stage artifacts so CI can
// distinguish a busy release from a rejected operation without parsing text.
const (
	// CodeOperationRejected identifies a formal API that refused the write.
	CodeOperationRejected = "operation_rejected"
	// CodeOperationFailed identifies a created operation that reached a
	// non-succeeded terminal state.
	CodeOperationFailed = "operation_failed"
	// CodeReleaseBusy identifies a target definition that already has a
	// non-terminal operation owned by another actor (REQ-066 release_busy).
	CodeReleaseBusy = "release_busy"
	// CodeCleanupTimeout identifies startup takeover or stage compensation
	// that did not complete inside the bounded grace window.
	CodeCleanupTimeout = "cleanup_timeout"
	// CodeEffectUnknown identifies an emergency effect that could not be
	// observed as applied or not-applied.
	CodeEffectUnknown = "emergency_effect_unknown"
	// CodeRestartTimeout identifies a restart barrier that did not converge.
	CodeRestartTimeout = "restart_barrier_timeout"
	// CodeCrossTargetChanged identifies a write that perturbed a target the
	// stage did not intend to modify (non-target invariance violation).
	CodeCrossTargetChanged = "cross_target_changed"
)

// RunnerActor is the stable principal string owned by the E2E dev account.
// Startup takeover only cancels non-terminal operations carrying this actor;
// any other actor is a genuine release_busy conflict (AC-066-27).
const RunnerActor = "e2e-runner"

// Terminal operation statuses normalized by normalizeOperationStatus.
const (
	statusSucceeded = "succeeded"
	statusFailed    = "failed"
	statusCancelled = "cancelled"
	statusTimeout   = "timeout"
)

// OperationRef is the sanitized public identity of one release operation.
// It never carries payload, values, or cluster-internal identifiers.
type OperationRef struct {
	ID           string
	DefinitionID string
	Type         string
	Status       string
	Revision     int32
	// Convergence is the effective emergency convergence policy reported for an
	// EMERGENCY operation. It is empty for standard operations.
	Convergence string
}

// Terminal reports whether the operation reached a terminal lifecycle state.
func (r OperationRef) Terminal() bool {
	switch normalizeOperationStatus(r.Status) {
	case statusSucceeded, statusFailed, statusCancelled, statusTimeout:
		return true
	default:
		return false
	}
}

// Succeeded reports whether the operation reached its success terminal state.
func (r OperationRef) Succeeded() bool {
	return normalizeOperationStatus(r.Status) == statusSucceeded
}

// ActiveOperation is one non-terminal operation attached to a release
// definition, used for startup takeover and release_busy detection.
type ActiveOperation struct {
	ID     string
	Type   string
	Status string
	Actor  string
}

// UpgradeRequest is the formal input for one UPGRADE operation.
type UpgradeRequest struct {
	DefinitionID     string
	BundleID         string
	ValuesRevisionID string
	// ExpectedRevision is the live observed revision read immediately before
	// the write (D-023 D6); it is never a static seed assertion.
	ExpectedRevision int32
}

// RollbackRequest is the formal input for one ROLLBACK operation.
type RollbackRequest struct {
	DefinitionID     string
	TargetRevision   int32
	ExpectedRevision int32
	Reason           string
}

// ReleaseObserver is the read-only formal-API seam consumed by write stages.
// Implementations wrap the generated Connect clients; tests inject fakes.
type ReleaseObserver interface {
	// Revision returns the live observed Helm revision for a definition.
	Revision(ctx context.Context, definitionID string) (int32, error)
	// ActiveOperation returns the definition's non-terminal operation when one
	// exists. The boolean is false when the definition is quiescent.
	ActiveOperation(ctx context.Context, definitionID string) (ActiveOperation, bool, error)
}

// OperationWriter is the write-side formal-API seam. Every method maps to a
// Connect RPC; no stage may bypass it with kubectl, helm, or os/exec.
type OperationWriter interface {
	// Upgrade creates an UPGRADE operation for the target definition.
	Upgrade(ctx context.Context, req UpgradeRequest) (OperationRef, error)
	// Rollback creates a ROLLBACK operation for the target definition.
	Rollback(ctx context.Context, req RollbackRequest) (OperationRef, error)
	// Cancel cancels a non-terminal operation, driving it to a legal terminal
	// state. It is idempotent for already-terminal operations.
	Cancel(ctx context.Context, operationID string) error
	// AwaitOperation polls one operation until it reaches a terminal state.
	// A nil error means the operation was observed to terminal; the caller must
	// still inspect OperationRef.Succeeded(). Non-nil errors report transport
	// failures or an exhausted stage deadline.
	AwaitOperation(ctx context.Context, operationID string) (OperationRef, error)
}

// WriteTarget is one reversible write target derived from the seed manifest.
type WriteTarget struct {
	// Name is the stage-facing seed name, e.g. "e2e-release-target".
	Name string
	// DefinitionID is the ReleaseDefinition the stage writes against.
	DefinitionID string
	// BundleID and ValuesRevisionID are the formal UPGRADE inputs from
	// seed.e2e_upgrade_targets. They are ignored by write stages that do not
	// upgrade a release.
	BundleID         string
	ValuesRevisionID string
}

// validate fails closed when a target is incomplete.
func (t WriteTarget) validate(component string) error {
	if strings.TrimSpace(t.DefinitionID) == "" {
		return newStageError(CodeSnapshotNotFound, component, "write target definition id missing")
	}
	return nil
}

// validateUpgrade additionally requires the formal UPGRADE inputs.
func (t WriteTarget) validateUpgrade(component string) error {
	if err := t.validate(component); err != nil {
		return err
	}
	if strings.TrimSpace(t.BundleID) == "" {
		return newStageError(CodeSnapshotNotFound, component, "write target bundle id missing")
	}
	if strings.TrimSpace(t.ValuesRevisionID) == "" {
		return newStageError(CodeSnapshotNotFound, component, "write target values revision id missing")
	}
	return nil
}

// normalizeOperationStatus folds the several public spellings of an operation
// status onto the lowercase lifecycle vocabulary used by this package. It
// accepts wire enum names ("OPERATION_STATUS_SUCCEEDED"), the state field's
// plain form ("succeeded"), and the historical uppercase variants.
func normalizeOperationStatus(status string) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	normalized = strings.TrimPrefix(normalized, "operation_status_")
	normalized = strings.TrimPrefix(normalized, "operation_status")
	normalized = strings.TrimPrefix(normalized, "status_")
	normalized = strings.TrimPrefix(normalized, "status")
	return strings.Trim(normalized, "_")
}

// activeOperationReader is the minimal read seam needed by startup takeover.
// ReleaseObserver and the emergency client both satisfy it.
type activeOperationReader interface {
	ActiveOperation(ctx context.Context, definitionID string) (ActiveOperation, bool, error)
}

// operationDriver is the minimal write seam needed to cancel and await an
// operation. OperationWriter and the emergency client both satisfy it.
type operationDriver interface {
	Cancel(ctx context.Context, operationID string) error
	AwaitOperation(ctx context.Context, operationID string) (OperationRef, error)
}

// takeover cancels a residual non-terminal operation owned by the E2E runner
// before a write stage creates its own operation (AC-066-27). An operation
// owned by any other actor is a release_busy conflict and is never cancelled.
func takeover(ctx context.Context, name, definitionID string, observer activeOperationReader, writer operationDriver) error {
	if observer == nil || writer == nil {
		return newStageError(CodeSnapshotNotFound, name, "operation observation unavailable")
	}
	if strings.TrimSpace(definitionID) == "" {
		return newStageError(CodeSnapshotNotFound, name, "write target definition id missing")
	}
	active, found, err := observer.ActiveOperation(ctx, definitionID)
	if err != nil {
		return newStageError(CodeSnapshotNotFound, name, "active operation observation failed")
	}
	if !found {
		return nil
	}
	if strings.TrimSpace(active.Actor) != RunnerActor {
		return newStageError(CodeReleaseBusy, name, "target definition has an active operation owned by another actor")
	}
	if strings.TrimSpace(active.ID) == "" {
		return newStageError(CodeSnapshotNotFound, name, "active operation identity missing")
	}
	if err := writer.Cancel(ctx, active.ID); err != nil {
		return newStageError(CodeCleanupTimeout, name, "startup takeover cancel failed")
	}
	terminal, err := writer.AwaitOperation(ctx, active.ID)
	if err != nil {
		return newStageError(CodeCleanupTimeout, name, "startup takeover did not reach a terminal state")
	}
	if !terminal.Terminal() {
		return newStageError(CodeCleanupTimeout, name, "startup takeover left a non-terminal operation")
	}
	return nil
}

// UpgradeGuard asserts an additional invariant around one reversible upgrade.
// It runs after the upgrade reached a succeeded terminal state and again after
// the compensating rollback, so a stage can prove that a target it did not
// intend to write was not perturbed by either operation (non-target
// invariance). A nil guard asserts nothing.
type UpgradeGuard func(ctx context.Context) error

// runUpgrade performs the shared reversible upgrade flow: startup takeover,
// dynamic revision read, UPGRADE creation, terminal await, and LIFO
// compensation registration that restores the observed baseline revision.
//
// The compensation is registered only after the upgrade succeeded, so a failed
// upgrade never rolls a release back beyond its baseline. Each write stage
// registers exactly one compensation under its own stable id.
func runUpgrade(
	ctx context.Context,
	name string,
	observer ReleaseObserver,
	writer OperationWriter,
	registry *e2e.CompensationRegistry,
	target WriteTarget,
	compensationID string,
	guard UpgradeGuard,
) (OperationRef, int32, error) {
	if err := target.validateUpgrade(name); err != nil {
		return OperationRef{}, 0, err
	}
	if registry == nil {
		return OperationRef{}, 0, newStageError(CodeSnapshotNotFound, name, "compensation registry unavailable")
	}
	if err := takeover(ctx, name, target.DefinitionID, observer, writer); err != nil {
		return OperationRef{}, 0, err
	}

	baseline, err := readBaselineRevision(ctx, name, observer, target.DefinitionID)
	if err != nil {
		return OperationRef{}, 0, err
	}

	terminal, err := executeUpgrade(ctx, name, writer, target, baseline)
	if err != nil {
		return terminal, baseline, err
	}
	if guard != nil {
		if err := guard(ctx); err != nil {
			return terminal, baseline, err
		}
	}

	// Compensation restores the observed baseline revision. It is bounded by
	// the caller's cleanup context and must never be the seed's assumed value.
	// The optimistic lock prefers the revision the terminal operation reported;
	// baseline+1 is only the fallback for adapters that do not surface it, since
	// revisions can legitimately skip.
	current := baseline + 1
	if terminal.Revision > 0 {
		current = terminal.Revision
	}
	restore := RollbackRequest{
		DefinitionID:     target.DefinitionID,
		TargetRevision:   baseline,
		ExpectedRevision: current,
		Reason:           "e2e " + name + " compensation",
	}
	if err := registerUpgradeCompensation(registry, compensationID, name, writer, restore, guard); err != nil {
		return terminal, baseline, err
	}

	return terminal, baseline, nil
}

// readBaselineRevision reads the target's live revision and rejects a value
// that cannot serve as a compensation anchor. The value is never assumed from
// the seed, which is what keeps expectedCurrentRevision honest across re-runs.
func readBaselineRevision(ctx context.Context, name string, observer ReleaseObserver, definitionID string) (int32, error) {
	baseline, err := observer.Revision(ctx, definitionID)
	if err != nil {
		return 0, newStageError(CodeSnapshotNotFound, name, "baseline revision observation failed")
	}
	if baseline <= 0 {
		return 0, newStageError(CodeSnapshotNotFound, name, fmt.Sprintf("baseline revision %d is not positive", baseline))
	}
	return baseline, nil
}

// executeUpgrade creates the UPGRADE against the observed revision and waits
// for a succeeded terminal state.
func executeUpgrade(ctx context.Context, name string, writer OperationWriter, target WriteTarget, baseline int32) (OperationRef, error) {
	created, err := writer.Upgrade(ctx, UpgradeRequest{
		DefinitionID:     target.DefinitionID,
		BundleID:         target.BundleID,
		ValuesRevisionID: target.ValuesRevisionID,
		ExpectedRevision: baseline,
	})
	if err != nil {
		return OperationRef{}, newStageError(CodeOperationRejected, name, "upgrade request rejected")
	}
	if strings.TrimSpace(created.ID) == "" {
		return OperationRef{}, newStageError(CodeOperationRejected, name, "upgrade response carried no operation id")
	}

	terminal, err := writer.AwaitOperation(ctx, created.ID)
	if err != nil {
		return created, newStageError(CodeOperationFailed, name, "upgrade did not reach a terminal state")
	}
	if !terminal.Succeeded() {
		return terminal, newStageError(CodeOperationFailed, name, "upgrade reached a non-succeeded terminal state")
	}
	return terminal, nil
}

// registerUpgradeCompensation registers the stage's single compensation: a
// rollback to the observed baseline. The guard, when set, is re-asserted after
// the rollback so a compensation that perturbs a non-target becomes Dirty
// instead of passing.
func registerUpgradeCompensation(
	registry *e2e.CompensationRegistry,
	compensationID string,
	name string,
	writer OperationWriter,
	restore RollbackRequest,
	guard UpgradeGuard,
) error {
	if err := registry.Register(compensationID, func(compCtx context.Context) error {
		compensation, err := writer.Rollback(compCtx, restore)
		if err != nil {
			return fmt.Errorf("%s: rollback compensation rejected", CodeCleanupTimeout)
		}
		result, err := writer.AwaitOperation(compCtx, compensation.ID)
		if err != nil {
			return fmt.Errorf("%s: rollback compensation not terminal", CodeCleanupTimeout)
		}
		if !result.Succeeded() {
			return fmt.Errorf("%s: rollback compensation did not succeed", CodeCleanupTimeout)
		}
		if guard != nil {
			if err := guard(compCtx); err != nil {
				return fmt.Errorf("%s: rollback compensation violated a stage invariant", CodeCleanupTimeout)
			}
		}
		return nil
	}); err != nil {
		return newStageError(CodeCleanupTimeout, name, "compensation registration failed")
	}
	return nil
}
