package stages

import (
	"context"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

// CompensationReleaseRollback restores the release target's baseline revision.
// The identity is stable so a repeated run cannot silently register the same
// restore twice (the registry rejects duplicates).
const CompensationReleaseRollback = "release:rollback"

// ReleaseStage performs one reversible UPGRADE against the release target and
// registers exactly one LIFO rollback compensation that restores the baseline
// revision observed immediately before the write (AC-066-02/13/22/31).
//
// The stage never mutates a cluster directly: every write and every
// observation goes through the formal Connect seams.
type ReleaseStage struct {
	observer ReleaseObserver
	writer   OperationWriter
	registry *e2e.CompensationRegistry
	target   WriteTarget

	result   OperationRef
	baseline int32
}

var _ e2e.Stage = (*ReleaseStage)(nil)

// NewReleaseStage creates the reversible release upgrade stage.
func NewReleaseStage(
	observer ReleaseObserver,
	writer OperationWriter,
	registry *e2e.CompensationRegistry,
	target WriteTarget,
) *ReleaseStage {
	return &ReleaseStage{observer: observer, writer: writer, registry: registry, target: target}
}

// NewRelease is a concise alias for NewReleaseStage.
func NewRelease(observer ReleaseObserver, writer OperationWriter, registry *e2e.CompensationRegistry, target WriteTarget) *ReleaseStage {
	return NewReleaseStage(observer, writer, registry, target)
}

// Name implements e2e.Stage.
func (s *ReleaseStage) Name() string { return "release" }

// Run implements e2e.Stage.
func (s *ReleaseStage) Run(ctx context.Context, _ *e2e.Fixture) error {
	if s == nil {
		return newStageError(CodeSnapshotNotFound, "release", "release stage unavailable")
	}
	result, baseline, err := runUpgrade(ctx, "release", s.observer, s.writer, s.registry, s.target, CompensationReleaseRollback, nil)
	if err != nil {
		return err
	}
	s.result = result
	s.baseline = baseline
	return nil
}

// Result returns the terminal UPGRADE operation reference.
func (s *ReleaseStage) Result() OperationRef {
	if s == nil {
		return OperationRef{}
	}
	return s.result
}

// BaselineRevision returns the revision observed before the upgrade.
func (s *ReleaseStage) BaselineRevision() int32 {
	if s == nil {
		return 0
	}
	return s.baseline
}

// UpgradedRevision returns the revision this stage left behind after a
// succeeded upgrade (baseline + 1), or 0 when the stage has not succeeded. A
// downstream stage binds it with IsolationStage.WithReleaseInvariant to prove
// the isolation write did not move the release target.
func (s *ReleaseStage) UpgradedRevision() int32 {
	if s == nil || !s.result.Succeeded() {
		return 0
	}
	return s.baseline + 1
}
