package stages

import (
	"context"
	"errors"
	"testing"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

func TestReleaseStageRun(t *testing.T) {
	t.Parallel()

	base := func() *writeFake {
		fake := newWriteFake()
		fake.await["op-upgrade"] = OperationRef{ID: "op-upgrade", Status: wireSucceeded}
		fake.await["op-rollback"] = OperationRef{ID: "op-rollback", Status: wireSucceeded}
		return fake
	}

	tests := []struct {
		name    string
		mutate  func(*writeFake)
		want    error
		wantReg int
	}{
		{name: "pass", wantReg: 1},
		{
			name:   "baseline revision unavailable",
			mutate: func(f *writeFake) { f.revisionErr = errors.New("private detail") },
			want:   ErrSnapshotNotFound,
		},
		{
			name:   "baseline revision not positive",
			mutate: func(f *writeFake) { f.revision = 0 },
			want:   ErrSnapshotNotFound,
		},
		{
			name:   "upgrade rejected",
			mutate: func(f *writeFake) { f.upgradeErr = errors.New("private detail") },
			want:   ErrOperationRejected,
		},
		{
			name: "upgrade failed terminally",
			mutate: func(f *writeFake) {
				f.await["op-upgrade"] = OperationRef{ID: "op-upgrade", Status: wireFailed}
			},
			want: ErrOperationFailed,
		},
		{
			name: "upgrade await deadline",
			mutate: func(f *writeFake) {
				f.awaitErr["op-upgrade"] = errors.New("deadline exceeded")
			},
			want: ErrOperationFailed,
		},
		{
			name: "foreign active operation",
			mutate: func(f *writeFake) {
				f.hasActive = true
				f.active = ActiveOperation{ID: "op-other", Actor: "user-1", Status: wireQueued}
			},
			want: ErrReleaseBusy,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fake := base()
			if test.mutate != nil {
				test.mutate(fake)
			}
			registry := e2e.NewCompensationRegistry()
			stage := NewReleaseStage(fake, fake, registry, releaseTarget())

			err := stage.Run(context.Background(), nil)
			if test.want == nil {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				if !stage.Result().Succeeded() {
					t.Fatalf("Result() = %#v, want succeeded", stage.Result())
				}
				if stage.BaselineRevision() != 3 {
					t.Fatalf("BaselineRevision() = %d, want 3", stage.BaselineRevision())
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want errors.Is(..., %v)", err, test.want)
			}
			if registry.Len() != test.wantReg {
				t.Fatalf("registry.Len() = %d, want %d", registry.Len(), test.wantReg)
			}
			// A failed stage must never schedule a rollback compensation.
			if test.want != nil && len(fake.rollbacks) != 0 {
				t.Fatalf("failed stage recorded rollbacks %v", fake.rollbacks)
			}
		})
	}
}

func TestReleaseStageTakeoverThenUpgrade(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	fake.hasActive = true
	fake.active = ActiveOperation{ID: "op-residual", Actor: RunnerActor, Status: wireQueued}
	fake.await["op-residual"] = OperationRef{ID: "op-residual", Status: wireCancelled}
	fake.await["op-upgrade"] = OperationRef{ID: "op-upgrade", Status: wireSucceeded}

	registry := e2e.NewCompensationRegistry()
	stage := NewReleaseStage(fake, fake, registry, releaseTarget())
	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(fake.cancels) != 1 || fake.cancels[0] != "op-residual" {
		t.Fatalf("cancels = %v, want the residual runner operation", fake.cancels)
	}
	if len(fake.upgrades) != 1 {
		t.Fatalf("upgrades = %d, want 1 after takeover", len(fake.upgrades))
	}
}

func TestReleaseStageMissingTarget(t *testing.T) {
	t.Parallel()

	registry := e2e.NewCompensationRegistry()
	stage := NewReleaseStage(newWriteFake(), newWriteFake(), registry, WriteTarget{DefinitionID: "def-release"})
	err := stage.Run(context.Background(), nil)
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("Run() error = %v, want ErrSnapshotNotFound for incomplete upgrade inputs", err)
	}
}

func TestReleaseStageNilRegistry(t *testing.T) {
	t.Parallel()

	stage := NewReleaseStage(newWriteFake(), newWriteFake(), nil, releaseTarget())
	err := stage.Run(context.Background(), nil)
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("Run() error = %v, want ErrSnapshotNotFound for a missing compensation registry", err)
	}
}

func TestRunUpgradeCompensationFailureIsDirty(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	fake.rollbackErr = errors.New("private rollback detail")
	registry := e2e.NewCompensationRegistry()

	if _, _, err := runUpgrade(context.Background(), "release", fake, fake, registry, releaseTarget(), CompensationReleaseRollback, nil); err != nil {
		t.Fatalf("runUpgrade() error = %v", err)
	}
	err := registry.Run(context.Background())
	if !errors.Is(err, e2e.ErrCompensationDirty) {
		t.Fatalf("registry.Run() error = %v, want ErrCompensationDirty", err)
	}
}

func TestRunUpgradeCompensationNotSucceededIsDirty(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	fake.await["op-rollback"] = OperationRef{ID: "op-rollback", Status: wireFailed}
	registry := e2e.NewCompensationRegistry()

	if _, _, err := runUpgrade(context.Background(), "release", fake, fake, registry, releaseTarget(), CompensationReleaseRollback, nil); err != nil {
		t.Fatalf("runUpgrade() error = %v", err)
	}
	if err := registry.Run(context.Background()); !errors.Is(err, e2e.ErrCompensationDirty) {
		t.Fatalf("registry.Run() error = %v, want ErrCompensationDirty", err)
	}
}

func TestIsolationStageRun(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	fake.await["op-upgrade"] = OperationRef{ID: "op-upgrade", Status: wireSucceeded}
	registry := e2e.NewCompensationRegistry()
	target := WriteTarget{
		Name:             "e2e-isolation-target",
		DefinitionID:     "def-isolation",
		BundleID:         "bundle-1",
		ValuesRevisionID: "values-1",
	}

	stage := NewIsolationStage(fake, fake, registry, target)
	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stage.Name() != "isolation" {
		t.Fatalf("Name() = %q, want isolation", stage.Name())
	}
	if len(fake.upgrades) != 1 || fake.upgrades[0].DefinitionID != "def-isolation" {
		t.Fatalf("upgrades = %#v, want the isolation definition", fake.upgrades)
	}
	if registry.Len() != 1 {
		t.Fatalf("registry.Len() = %d, want 1", registry.Len())
	}
	if err := registry.Run(context.Background()); err != nil {
		t.Fatalf("registry.Run() error = %v", err)
	}
	if len(fake.rollbacks) != 1 || fake.rollbacks[0].DefinitionID != "def-isolation" {
		t.Fatalf("rollbacks = %#v, want the isolation definition", fake.rollbacks)
	}
}

func TestIsolationAndReleaseCompensationsCoexistLIFO(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	fake.await["op-upgrade"] = OperationRef{ID: "op-upgrade", Status: wireSucceeded}
	fake.await["op-rollback"] = OperationRef{ID: "op-rollback", Status: wireSucceeded}
	registry := e2e.NewCompensationRegistry()

	release := NewReleaseStage(fake, fake, registry, releaseTarget())
	isolation := NewIsolationStage(fake, fake, registry, WriteTarget{
		Name:             "e2e-isolation-target",
		DefinitionID:     "def-isolation",
		BundleID:         "bundle-1",
		ValuesRevisionID: "values-1",
	})

	if err := release.Run(context.Background(), nil); err != nil {
		t.Fatalf("release Run() error = %v", err)
	}
	if err := isolation.Run(context.Background(), nil); err != nil {
		t.Fatalf("isolation Run() error = %v", err)
	}
	if registry.Len() != 2 {
		t.Fatalf("registry.Len() = %d, want one compensation per write stage", registry.Len())
	}
	if err := registry.Run(context.Background()); err != nil {
		t.Fatalf("registry.Run() error = %v", err)
	}
	// LIFO: isolation (registered last) must be compensated first.
	if len(fake.rollbacks) != 2 {
		t.Fatalf("rollbacks = %d, want 2", len(fake.rollbacks))
	}
	if fake.rollbacks[0].DefinitionID != "def-isolation" || fake.rollbacks[1].DefinitionID != "def-release" {
		t.Fatalf("rollback order = %#v, want isolation before release", fake.rollbacks)
	}
}

func TestIsolationStageReleaseInvariantHolds(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	fake.revisions["def-release"] = 4
	fake.revisions["def-isolation"] = 3
	fake.await["op-upgrade"] = OperationRef{ID: "op-upgrade", Status: wireSucceeded}
	fake.await["op-rollback"] = OperationRef{ID: "op-rollback", Status: wireSucceeded}
	registry := e2e.NewCompensationRegistry()

	stage := NewIsolationStage(fake, fake, registry, WriteTarget{
		Name:             "e2e-isolation-target",
		DefinitionID:     "def-isolation",
		BundleID:         "bundle-1",
		ValuesRevisionID: "values-1",
	}).WithReleaseInvariant("def-release", 4)

	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := registry.Run(context.Background()); err != nil {
		t.Fatalf("registry.Run() error = %v", err)
	}
	// Two isolation reads (baseline) + two release invariant reads (after the
	// upgrade and after the rollback).
	if got := fake.revisionCalls["def-release"]; got != 2 {
		t.Fatalf("release invariant observations = %d, want one after the upgrade and one after the rollback", got)
	}
}

func TestIsolationStageReleaseInvariantViolatedAfterUpgrade(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	fake.revisionDrift["def-release"] = revisionDrift{afterCall: 1, value: 4}
	fake.await["op-upgrade"] = OperationRef{ID: "op-upgrade", Status: wireSucceeded}
	registry := e2e.NewCompensationRegistry()

	stage := NewIsolationStage(fake, fake, registry, WriteTarget{
		Name:             "e2e-isolation-target",
		DefinitionID:     "def-isolation",
		BundleID:         "bundle-1",
		ValuesRevisionID: "values-1",
	}).WithReleaseInvariant("def-release", 3)

	err := stage.Run(context.Background(), nil)
	if !errors.Is(err, ErrCrossTargetChanged) {
		t.Fatalf("Run() error = %v, want ErrCrossTargetChanged", err)
	}
	if registry.Len() != 0 {
		t.Fatalf("registry.Len() = %d, want no compensation after a cross-target change", registry.Len())
	}
}

func TestIsolationStageReleaseInvariantViolatedByCompensation(t *testing.T) {
	t.Parallel()

	fake := newWriteFake()
	fake.revisions["def-release"] = 3
	// The invariant holds after the upgrade (call 1) and fails after the
	// compensating rollback (call 2).
	fake.revisionDrift["def-release"] = revisionDrift{afterCall: 2, value: 4}
	fake.await["op-upgrade"] = OperationRef{ID: "op-upgrade", Status: wireSucceeded}
	fake.await["op-rollback"] = OperationRef{ID: "op-rollback", Status: wireSucceeded}
	registry := e2e.NewCompensationRegistry()

	stage := NewIsolationStage(fake, fake, registry, WriteTarget{
		Name:             "e2e-isolation-target",
		DefinitionID:     "def-isolation",
		BundleID:         "bundle-1",
		ValuesRevisionID: "values-1",
	}).WithReleaseInvariant("def-release", 3)

	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := registry.Run(context.Background()); !errors.Is(err, e2e.ErrCompensationDirty) {
		t.Fatalf("registry.Run() error = %v, want ErrCompensationDirty", err)
	}
}

func TestIsolationStageReleaseInvariantConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		definition string
		revision   int32
		target     string
		wantErr    bool
	}{
		{name: "valid", definition: "def-release", revision: 4, target: "def-isolation"},
		{name: "non positive revision", definition: "def-release", revision: 0, target: "def-isolation", wantErr: true},
		{name: "aliases isolation target", definition: "def-isolation", revision: 4, target: "def-isolation", wantErr: true},
		{name: "unset guard is allowed", definition: "", revision: 0, target: "def-isolation"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			registry := e2e.NewCompensationRegistry()
			fake := newWriteFake()
			if test.definition != "" {
				fake.revisions[test.definition] = test.revision
			}
			stage := NewIsolationStage(fake, fake, registry, WriteTarget{
				Name:             "e2e-isolation-target",
				DefinitionID:     test.target,
				BundleID:         "bundle-1",
				ValuesRevisionID: "values-1",
			})
			if test.definition != "" {
				stage = stage.WithReleaseInvariant(test.definition, test.revision)
			}
			err := stage.Run(context.Background(), nil)
			if test.wantErr {
				if !errors.Is(err, ErrFixtureStale) {
					t.Fatalf("Run() error = %v, want ErrFixtureStale", err)
				}
				if registry.Len() != 0 {
					t.Fatalf("registry.Len() = %d, want no compensation for a rejected configuration", registry.Len())
				}
				return
			}
			if err != nil {
				t.Fatalf("Run() error = %v, want nil", err)
			}
			if registry.Len() != 1 {
				t.Fatalf("registry.Len() = %d, want exactly one isolation compensation", registry.Len())
			}
		})
	}
}

func TestValidateIsolationTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		release   WriteTarget
		isolation WriteTarget
		want      error
	}{
		{
			name:      "distinct",
			release:   WriteTarget{DefinitionID: "def-release"},
			isolation: WriteTarget{DefinitionID: "def-isolation"},
		},
		{
			name:      "same definition",
			release:   WriteTarget{DefinitionID: "def-shared"},
			isolation: WriteTarget{DefinitionID: "def-shared"},
			want:      ErrFixtureStale,
		},
		{
			name:      "missing isolation",
			release:   WriteTarget{DefinitionID: "def-release"},
			isolation: WriteTarget{},
			want:      ErrSnapshotNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateIsolationTargets(test.release, test.isolation)
			if test.want == nil {
				if err != nil {
					t.Fatalf("ValidateIsolationTargets() error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("ValidateIsolationTargets() error = %v, want %v", err, test.want)
			}
		})
	}
}
