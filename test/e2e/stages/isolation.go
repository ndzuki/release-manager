package stages

import (
	"context"
	"strings"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

// CompensationIsolationRollback restores the isolation target's baseline
// revision.
const CompensationIsolationRollback = "isolation:rollback"

// IsolationStage performs the same reversible upgrade flow as ReleaseStage on
// the dedicated isolation target. Binding it to a distinct definition is what
// makes the isolation assertion meaningful: the isolation upgrade must not
// leak into the release target's inventory row (AC-066-14/15/18).
type IsolationStage struct {
	observer ReleaseObserver
	writer   OperationWriter
	registry *e2e.CompensationRegistry
	target   WriteTarget

	result   OperationRef
	baseline int32
}

var _ e2e.Stage = (*IsolationStage)(nil)

// NewIsolationStage creates the reversible isolation upgrade stage.
func NewIsolationStage(
	observer ReleaseObserver,
	writer OperationWriter,
	registry *e2e.CompensationRegistry,
	target WriteTarget,
) *IsolationStage {
	return &IsolationStage{observer: observer, writer: writer, registry: registry, target: target}
}

// NewIsolation is a concise alias for NewIsolationStage.
func NewIsolation(observer ReleaseObserver, writer OperationWriter, registry *e2e.CompensationRegistry, target WriteTarget) *IsolationStage {
	return NewIsolationStage(observer, writer, registry, target)
}

// Name implements e2e.Stage.
func (s *IsolationStage) Name() string { return "isolation" }

// Run implements e2e.Stage.
func (s *IsolationStage) Run(ctx context.Context, _ *e2e.Fixture) error {
	if s == nil {
		return newStageError(CodeSnapshotNotFound, "isolation", "isolation stage unavailable")
	}
	result, baseline, err := runUpgrade(ctx, "isolation", s.observer, s.writer, s.registry, s.target, CompensationIsolationRollback)
	if err != nil {
		return err
	}
	s.result = result
	s.baseline = baseline
	return nil
}

// Result returns the terminal isolation UPGRADE operation reference.
func (s *IsolationStage) Result() OperationRef {
	if s == nil {
		return OperationRef{}
	}
	return s.result
}

// BaselineRevision returns the revision observed before the isolation upgrade.
func (s *IsolationStage) BaselineRevision() int32 {
	if s == nil {
		return 0
	}
	return s.baseline
}

// ValidateIsolationTargets refuses a configuration that binds the release and
// isolation stages to the same definition: the isolation assertion would then
// be vacuous, and the two compensations would race on one release.
func ValidateIsolationTargets(release, isolation WriteTarget) error {
	if strings.TrimSpace(release.DefinitionID) == "" || strings.TrimSpace(isolation.DefinitionID) == "" {
		return newStageError(CodeSnapshotNotFound, "isolation", "release and isolation targets must both declare a definition id")
	}
	if release.DefinitionID == isolation.DefinitionID {
		return newStageError(CodeFixtureStale, "isolation", "release and isolation targets must be distinct definitions")
	}
	return nil
}
