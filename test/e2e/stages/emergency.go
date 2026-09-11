package stages

import (
	"context"
	"fmt"
	"strings"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

// Emergency convergence policies. REVERT_ON_NEXT_RECONCILE is the default for
// reversible replica tests; REQUIRE_PROMOTION is only used by a deliberate
// promotion mapping and would create a Convergence Task (AC-066-16).
const (
	EmergencyConvergenceRevertOnNextReconcile = "REVERT_ON_NEXT_RECONCILE"
	EmergencyConvergenceRequirePromotion      = "REQUIRE_PROMOTION"
)

// CompensationEmergencyRestore restores the emergency target's baseline
// replica count through the same formal emergency API.
const CompensationEmergencyRestore = "emergency:restore"

// EmergencyTarget is one observable emergency-eligible workload. Targets are
// read from the formal API rather than derived from a release name, so the
// workload identity cannot drift from the deployed chart (AC-066-07).
type EmergencyTarget struct {
	WorkloadKind    string
	WorkloadName    string
	Namespace       string
	CurrentReplicas int32
	// OperationVersion is an optional adapter-supplied version hint echoed on
	// the write. The live API does not report one on ListEmergencyTargets, so
	// the adapter leaves it empty and the server derives the authoritative
	// version on acceptance (REQ-079 D4); when it is set it must match the
	// server's semver-shaped OperationVersionSchema or the write is rejected.
	OperationVersion string
	// Reference is the authoritative "<gvr.resource>/<namespace>/<name>" string
	// the formal emergency API expects. The adapter resolves it from the
	// cluster's REST mapping (kind -> plural resource); the stage never guesses
	// it, because "deployment/ns/name" is not the same reference as
	// "deployments/ns/name" and a wrong form is rejected server-side.
	Reference string
}

// WorkloadKey returns a stable "<kind>/<namespace>/<name>" diagnostic label.
// It is for assertions and artifact detail only: it is NOT the API reference
// (see Reference), which carries the plural GVR resource.
func (t EmergencyTarget) WorkloadKey() string {
	return fmt.Sprintf("%s/%s/%s", strings.ToLower(strings.TrimSpace(t.WorkloadKind)), strings.TrimSpace(t.Namespace), strings.TrimSpace(t.WorkloadName))
}

// WorkloadReference returns the authoritative API reference for this target,
// falling back to the diagnostic key only when the adapter did not resolve one.
func (t EmergencyTarget) WorkloadReference() string {
	if reference := strings.TrimSpace(t.Reference); reference != "" {
		return reference
	}
	return t.WorkloadKey()
}

// ReplicaObservation is the observed replica state of one workload.
type ReplicaObservation struct {
	Workload string
	Replicas int32
	Ready    int32
}

// EmergencySetReplicasRequest is the formal input for one SET_REPLICAS change.
type EmergencySetReplicasRequest struct {
	DefinitionID string
	// WorkloadRef is the authoritative "<gvr.resource>/<namespace>/<name>"
	// reference. Callers pass EmergencyTarget.WorkloadReference().
	WorkloadRef      string
	Replicas         int32
	Convergence      string
	OperationVersion string
}

// EmergencyWriter is the formal-API seam for reversible emergency changes.
type EmergencyWriter interface {
	ActiveOperation(ctx context.Context, definitionID string) (ActiveOperation, bool, error)
	Targets(ctx context.Context, definitionID string) ([]EmergencyTarget, error)
	SetReplicas(ctx context.Context, req EmergencySetReplicasRequest) (OperationRef, error)
	Cancel(ctx context.Context, operationID string) error
	AwaitOperation(ctx context.Context, operationID string) (OperationRef, error)
}

// ReplicaObserver observes the applied cluster effect of a replica change. It
// is read-only; a missing or unknown observation is fail-closed.
type ReplicaObserver interface {
	ObserveReplicas(ctx context.Context, namespace, workloadName string) (ReplicaObservation, error)
}

// EmergencyStage performs one reversible SET_REPLICAS emergency change that is
// visibly different from the baseline, verifies the applied effect through a
// read-only observer, and registers exactly one restore compensation.
type EmergencyStage struct {
	writer   EmergencyWriter
	observer ReplicaObserver
	registry *e2e.CompensationRegistry
	target   WriteTarget
	replicas int32
	baseline int32
	workload EmergencyTarget
	result   OperationRef
}

var _ e2e.Stage = (*EmergencyStage)(nil)

// NewEmergencyStage creates the reversible emergency replica stage.
func NewEmergencyStage(
	writer EmergencyWriter,
	observer ReplicaObserver,
	registry *e2e.CompensationRegistry,
	target WriteTarget,
	replicas int32,
) *EmergencyStage {
	return &EmergencyStage{writer: writer, observer: observer, registry: registry, target: target, replicas: replicas}
}

// NewEmergency is a concise alias for NewEmergencyStage.
func NewEmergency(writer EmergencyWriter, observer ReplicaObserver, registry *e2e.CompensationRegistry, target WriteTarget, replicas int32) *EmergencyStage {
	return NewEmergencyStage(writer, observer, registry, target, replicas)
}

// Name implements e2e.Stage.
func (s *EmergencyStage) Name() string { return "emergency" }

// Run implements e2e.Stage.
func (s *EmergencyStage) Run(ctx context.Context, _ *e2e.Fixture) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := takeover(ctx, "emergency", s.target.DefinitionID, s.writer, s.writer); err != nil {
		return err
	}

	workload, baseline, err := s.resolveBaseline(ctx)
	if err != nil {
		return err
	}
	terminal, err := s.applyChange(ctx, workload)
	if err != nil {
		return err
	}
	// The operation terminal state is not the cluster effect: assert the
	// observed workload actually moved to the requested replica count.
	if err := s.assertReplicas(ctx, workload, s.replicas); err != nil {
		return err
	}
	if err := s.registerRestore(workload, baseline); err != nil {
		return err
	}

	s.workload = workload
	s.baseline = baseline
	s.result = terminal
	return nil
}

// validate fails closed on an unusable stage configuration.
func (s *EmergencyStage) validate() error {
	if s == nil || s.writer == nil || s.observer == nil {
		return newStageError(CodeSnapshotNotFound, "emergency", "emergency stage unavailable")
	}
	if err := s.target.validate("emergency"); err != nil {
		return err
	}
	if s.registry == nil {
		return newStageError(CodeSnapshotNotFound, "emergency", "compensation registry unavailable")
	}
	if s.replicas <= 0 {
		return newStageError(CodeSnapshotNotFound, "emergency", "target replica count must be positive")
	}
	return nil
}

// resolveBaseline selects the single emergency workload and validates that its
// observed replica count can serve as a meaningful baseline.
//
// The count comes from the read-only cluster observation, not from the target's
// CurrentReplicas: ListEmergencyTargets reports that field as a D7=A unavailable
// sentinel (-1) by contract, so treating it as an observation can only ever fail
// as "not positive" (real smoke 2026-09-11: it rejected every emergency run
// while the workload was running fine).
func (s *EmergencyStage) resolveBaseline(ctx context.Context) (EmergencyTarget, int32, error) {
	workload, err := s.selectTarget(ctx)
	if err != nil {
		return EmergencyTarget{}, 0, err
	}
	observation, err := s.observer.ObserveReplicas(ctx, workload.Namespace, workload.WorkloadName)
	if err != nil {
		return EmergencyTarget{}, 0, newStageError(CodeSnapshotNotFound, "emergency", "baseline replica observation failed")
	}
	baseline := observation.Replicas
	if baseline <= 0 {
		return EmergencyTarget{}, 0, newStageError(CodeSnapshotNotFound, "emergency", "baseline replica count is not positive")
	}
	if baseline == s.replicas {
		return EmergencyTarget{}, 0, newStageError(CodeFixtureStale, "emergency", "target replica count equals the baseline; the change would not be observable")
	}
	return workload, baseline, nil
}

// applyChange executes the SET_REPLICAS emergency change and waits for it to
// reach a succeeded terminal state with the requested convergence policy.
func (s *EmergencyStage) applyChange(ctx context.Context, workload EmergencyTarget) (OperationRef, error) {
	created, err := s.writer.SetReplicas(ctx, EmergencySetReplicasRequest{
		DefinitionID:     s.target.DefinitionID,
		WorkloadRef:      workload.WorkloadReference(),
		Replicas:         s.replicas,
		Convergence:      EmergencyConvergenceRevertOnNextReconcile,
		OperationVersion: workload.OperationVersion,
	})
	if err != nil {
		return OperationRef{}, newStageError(CodeOperationRejected, "emergency", "set_replicas request rejected")
	}
	if strings.TrimSpace(created.ID) == "" {
		return OperationRef{}, newStageError(CodeOperationRejected, "emergency", "set_replicas response carried no operation id")
	}

	terminal, err := s.writer.AwaitOperation(ctx, created.ID)
	if err != nil {
		return OperationRef{}, newStageError(CodeOperationFailed, "emergency", "emergency operation did not reach a terminal state")
	}
	if !terminal.Succeeded() {
		return OperationRef{}, newStageError(CodeOperationFailed, "emergency", "emergency operation reached a non-succeeded terminal state")
	}
	// ADR-011: the effective convergence policy is a safety property of the
	// change, not a hint. When the operation reports one it must match what the
	// stage requested, otherwise a promotion-backed change would silently
	// persist instead of reverting on the next reconcile.
	if policy := strings.TrimSpace(terminal.Convergence); policy != "" && !strings.EqualFold(policy, EmergencyConvergenceRevertOnNextReconcile) {
		return OperationRef{}, newStageError(CodeEffectUnknown, "emergency", fmt.Sprintf("convergence policy %q is not the requested revert policy", policy))
	}
	return terminal, nil
}

// selectTarget resolves the emergency workload from the formal target list.
// Multiple eligible targets are ambiguous and fail closed rather than picking
// one, so the stage always operates on exactly one workload; non-target
// invariance for emergency is therefore structural (see the package doc note in
// restart.go for the cross-stage guards that do need an explicit assertion).
func (s *EmergencyStage) selectTarget(ctx context.Context) (EmergencyTarget, error) {
	targets, err := s.writer.Targets(ctx, s.target.DefinitionID)
	if err != nil {
		return EmergencyTarget{}, newStageError(CodeSnapshotNotFound, "emergency", "emergency target observation failed")
	}
	eligible := make([]EmergencyTarget, 0, len(targets))
	for _, target := range targets {
		if strings.TrimSpace(target.WorkloadName) == "" || strings.TrimSpace(target.Namespace) == "" {
			continue
		}
		eligible = append(eligible, target)
	}
	switch len(eligible) {
	case 0:
		return EmergencyTarget{}, newStageError(CodeSnapshotNotFound, "emergency", "no emergency workload target observed")
	case 1:
		return eligible[0], nil
	default:
		return EmergencyTarget{}, newStageError(CodeEffectUnknown, "emergency", fmt.Sprintf("ambiguous emergency targets: %d observed", len(eligible)))
	}
}

// assertReplicas verifies the applied effect through the read-only observer.
func (s *EmergencyStage) assertReplicas(ctx context.Context, workload EmergencyTarget, want int32) error {
	observation, err := s.observer.ObserveReplicas(ctx, workload.Namespace, workload.WorkloadName)
	if err != nil {
		return newStageError(CodeEffectUnknown, "emergency", "replica observation failed")
	}
	if observation.Replicas != want {
		return newStageError(CodeEffectUnknown, "emergency", fmt.Sprintf("replicas expected %d got %d", want, observation.Replicas))
	}
	if observation.Ready != want {
		return newStageError(CodeEffectUnknown, "emergency", fmt.Sprintf("ready replicas expected %d got %d", want, observation.Ready))
	}
	return nil
}

// registerRestore registers the single LIFO compensation that returns the
// workload to its observed baseline replica count.
func (s *EmergencyStage) registerRestore(workload EmergencyTarget, baseline int32) error {
	restore := EmergencySetReplicasRequest{
		DefinitionID: s.target.DefinitionID,
		WorkloadRef:  workload.WorkloadReference(),
		Replicas:     baseline,
		Convergence:  EmergencyConvergenceRevertOnNextReconcile,
	}
	if err := s.registry.Register(CompensationEmergencyRestore, func(compCtx context.Context) error {
		compensation, err := s.writer.SetReplicas(compCtx, restore)
		if err != nil {
			return fmt.Errorf("%s: emergency restore rejected", CodeCleanupTimeout)
		}
		result, err := s.writer.AwaitOperation(compCtx, compensation.ID)
		if err != nil {
			return fmt.Errorf("%s: emergency restore not terminal", CodeCleanupTimeout)
		}
		if !result.Succeeded() {
			return fmt.Errorf("%s: emergency restore did not succeed", CodeCleanupTimeout)
		}
		if err := s.assertReplicas(compCtx, workload, baseline); err != nil {
			return fmt.Errorf("%s: emergency restore effect not observed", CodeCleanupTimeout)
		}
		return nil
	}); err != nil {
		return newStageError(CodeCleanupTimeout, "emergency", "compensation registration failed")
	}
	return nil
}

// Result returns the terminal emergency operation reference.
func (s *EmergencyStage) Result() OperationRef {
	if s == nil {
		return OperationRef{}
	}
	return s.result
}

// BaselineReplicas returns the replica count observed before the change.
func (s *EmergencyStage) BaselineReplicas() int32 {
	if s == nil {
		return 0
	}
	return s.baseline
}

// Workload returns the resolved emergency workload target.
func (s *EmergencyStage) Workload() EmergencyTarget {
	if s == nil {
		return EmergencyTarget{}
	}
	return s.workload
}
