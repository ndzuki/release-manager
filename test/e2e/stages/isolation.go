package stages

import (
	"context"
	"fmt"
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

	// invariantDefinitionID and invariantRevision pin the release target that
	// this stage must leave untouched. Empty means no invariance guard is
	// configured, in which case the isolation assertion is limited to the
	// distinct-definition check in ValidateIsolationTargets.
	invariantDefinitionID string
	invariantRevision     int32

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

// WithReleaseInvariant pins the release target's post-upgrade revision and
// requires the isolation stage to observe it unchanged after its own upgrade
// and again after its compensating rollback. Wire it from a succeeded
// ReleaseStage with WithReleaseInvariant(releaseTarget.DefinitionID,
// release.UpgradedRevision()).
//
// The guard is what makes "isolation" an observed property rather than a
// naming convention: without it a mis-bound target would leave the release
// inventory row moved and still be reported as passing.
func (s *IsolationStage) WithReleaseInvariant(definitionID string, revision int32) *IsolationStage {
	if s == nil {
		return nil
	}
	s.invariantDefinitionID = strings.TrimSpace(definitionID)
	s.invariantRevision = revision
	return s
}

// Name implements e2e.Stage.
func (s *IsolationStage) Name() string { return "isolation" }

// Run implements e2e.Stage.
func (s *IsolationStage) Run(ctx context.Context, _ *e2e.Fixture) error {
	if s == nil {
		return newStageError(CodeSnapshotNotFound, "isolation", "isolation stage unavailable")
	}
	if err := s.validateInvariant(); err != nil {
		return err
	}
	result, baseline, err := runUpgrade(ctx, "isolation", s.observer, s.writer, s.registry, s.target, CompensationIsolationRollback, s.assertReleaseUnchanged)
	if err != nil {
		return err
	}
	s.result = result
	s.baseline = baseline
	return nil
}

// validateInvariant fails closed on a half-configured guard.
func (s *IsolationStage) validateInvariant() error {
	if s.invariantDefinitionID == "" {
		return nil
	}
	if s.invariantRevision <= 0 {
		return newStageError(CodeFixtureStale, "isolation", "release invariant revision must be positive")
	}
	if s.invariantDefinitionID == s.target.DefinitionID {
		return newStageError(CodeFixtureStale, "isolation", "release invariant must not alias the isolation definition")
	}
	return nil
}

// assertReleaseUnchanged is the UpgradeGuard bound by WithReleaseInvariant.
func (s *IsolationStage) assertReleaseUnchanged(ctx context.Context) error {
	if s.invariantDefinitionID == "" {
		return nil
	}
	revision, err := s.observer.Revision(ctx, s.invariantDefinitionID)
	if err != nil {
		return newStageError(CodeSnapshotNotFound, "isolation", "release target revision observation failed")
	}
	if revision != s.invariantRevision {
		return newStageError(CodeCrossTargetChanged, "isolation", fmt.Sprintf("release definition %s revision expected %d got %d", s.invariantDefinitionID, s.invariantRevision, revision))
	}
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
