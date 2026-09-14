package e2e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunner_AllPass(t *testing.T) {
	t.Parallel()

	s1 := NewStage("stage-1", func(_ context.Context, f *Fixture) error {
		f.Set("s1", true)
		return nil
	})
	s2 := NewStage("stage-2", func(_ context.Context, f *Fixture) error {
		f.Set("s2", true)
		return nil
	})

	r := NewRunner(s1, s2)
	f := NewFixture()
	res := r.Run(context.Background(), f)

	assert.True(t, res.AllPassed())
	assert.Equal(t, 0, res.Failed)
	assert.Equal(t, 0, res.Skipped)
	require.Len(t, res.Results, 2)
	assert.Equal(t, StagePass, res.Results[0].Status)
	assert.Equal(t, StagePass, res.Results[1].Status)
	v, ok := f.Get("s1").(bool)
	require.True(t, ok && v)
	v, ok = f.Get("s2").(bool)
	require.True(t, ok && v)
}

func TestRunner_SkipOnFail(t *testing.T) {
	// AC-066-01: Given control-plane stage fails, subsequent stages
	// can skip with reason.
	t.Parallel()

	failErr := errors.New("control-plane unavailable")

	s1 := NewStage("control-plane", func(_ context.Context, f *Fixture) error {
		f.Set("cp", "started")
		return failErr
	})
	s2 := NewStage("inventory", func(_ context.Context, f *Fixture) error {
		f.Set("inv", "loaded")
		return nil
	})
	s3 := NewStage("artifact", func(_ context.Context, f *Fixture) error {
		f.Set("art", "ingested")
		return nil
	})

	r := NewRunner(s1, s2, s3)
	r.StageTimeout = 10 * time.Second
	f := NewFixture()
	res := r.Run(context.Background(), f)

	assert.False(t, res.AllPassed())
	assert.Equal(t, 1, res.Failed)
	assert.Equal(t, 2, res.Skipped)

	require.Len(t, res.Results, 3)

	// Stage 1: fail.
	assert.Equal(t, StageFail, res.Results[0].Status)
	assert.Equal(t, "control-plane", res.Results[0].Name)
	assert.Contains(t, res.Results[0].Cause, "control-plane unavailable")

	// Stage 2: skip.
	assert.Equal(t, StageSkip, res.Results[1].Status)
	assert.Equal(t, "inventory", res.Results[1].Name)
	assert.Contains(t, res.Results[1].Cause, "previous stage failed")

	// Stage 3: skip.
	assert.Equal(t, StageSkip, res.Results[2].Status)
	assert.Equal(t, "artifact", res.Results[2].Name)
	assert.Contains(t, res.Results[2].Cause, "previous stage failed")
}

func TestRunner_TimeoutPreservesCompleted(t *testing.T) {
	// AC-066-04: Given e2e_timeout, current stage terminates and
	// preserves completed stage results.
	t.Parallel()

	s1 := NewStage("fast-stage", func(_ context.Context, f *Fixture) error {
		f.Set("fast", "done")
		return nil
	})
	s2 := NewStage("stuck-stage", func(ctx context.Context, _ *Fixture) error {
		<-ctx.Done()
		return ctx.Err()
	})

	r := NewRunner(s1, s2)
	r.StageTimeout = 50 * time.Millisecond
	f := NewFixture()
	res := r.Run(context.Background(), f)

	assert.False(t, res.AllPassed())
	require.Len(t, res.Results, 2)

	// Stage 1: pass.
	assert.Equal(t, StagePass, res.Results[0].Status)
	assert.Equal(t, "fast-stage", res.Results[0].Name)
	s, ok := f.Get("fast").(string)
	require.True(t, ok && s == "done")

	// Stage 2: fail due to timeout.
	assert.Equal(t, StageFail, res.Results[1].Status)
	assert.Equal(t, "stuck-stage", res.Results[1].Name)
	assert.Contains(t, res.Results[1].Cause, "timeout")
}

func TestRunner_NoTimeout(t *testing.T) {
	t.Parallel()

	s1 := NewStage("quick", func(_ context.Context, f *Fixture) error {
		f.Set("ok", true)
		return nil
	})

	r := NewRunner(s1)
	r.StageTimeout = 0 // no deadline
	f := NewFixture()
	res := r.Run(context.Background(), f)

	assert.True(t, res.AllPassed())
	v, ok := f.Get("ok").(bool)
	require.True(t, ok && v)
}

func TestRunner_SkipOnFailDisabled(t *testing.T) {
	t.Parallel()

	s1 := NewStage("failing", func(_ context.Context, _ *Fixture) error {
		return errors.New("boom")
	})
	s2 := NewStage("still-runs", func(_ context.Context, f *Fixture) error {
		f.Set("ran", true)
		return nil
	})

	r := NewRunner(s1, s2)
	r.SkipOnFail = false
	f := NewFixture()
	res := r.Run(context.Background(), f)

	assert.Equal(t, 1, res.Failed)
	assert.Equal(t, 0, res.Skipped)
	v, ok := f.Get("ran").(bool)
	require.True(t, ok && v)
}

func TestFixture_CloneIsIndependent(t *testing.T) {
	t.Parallel()

	f := NewFixture()
	f.Set("a", 1)

	f2 := f.Clone()
	f2.Set("a", 2)
	f2.Set("b", 3)

	assert.Equal(t, 1, f.Get("a"))
	assert.Equal(t, 2, f2.Get("a"))
	assert.False(t, f.Has("b"))
	assert.True(t, f2.Has("b"))
}

func TestHarness_ExplicitSelectionDoesNotRunPrerequisites(t *testing.T) {
	t.Parallel()

	var releaseRan bool
	h := NewHarness(
		StageSpec{
			Name: StageInventory,
			Stage: NewStage(StageInventory, func(context.Context, *Fixture) error {
				return errors.New("inventory unavailable")
			}),
		},
		StageSpec{
			Name: StageRelease,
			Stage: NewStage(StageRelease, func(context.Context, *Fixture) error {
				releaseRan = true
				return nil
			}),
			Dependencies: []string{StageInventory},
		},
	)

	report, err := h.Run(context.Background(), Scenario{SelectedStages: []string{StageRelease}})
	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	assert.True(t, releaseRan)
	assert.Equal(t, StagePass, report.Results[0].Status)
}

func TestHarness_DependencyFailuresSkipOnlyDescendants(t *testing.T) {
	t.Parallel()

	called := make(map[string]bool)
	stage := func(name string, err error) Stage {
		return NewStage(name, func(context.Context, *Fixture) error {
			called[name] = true
			return err
		})
	}

	h := NewHarness(
		StageSpec{Name: StageControlPlane, Stage: stage(StageControlPlane, errors.New("control plane down"))},
		StageSpec{Name: StageInventory, Stage: stage(StageInventory, nil)},
		StageSpec{Name: StageArtifactName, Stage: stage(StageArtifactName, nil)},
		StageSpec{Name: StageRelease, Stage: stage(StageRelease, nil)},
		StageSpec{Name: StageIsolation, Stage: stage(StageIsolation, nil)},
		StageSpec{Name: StageEmergency, Stage: stage(StageEmergency, nil)},
		StageSpec{Name: StageRestart, Stage: stage(StageRestart, nil)},
	)

	report, err := h.Run(context.Background(), Scenario{
		SelectedStages: []string{
			StageControlPlane,
			StageInventory,
			StageArtifactName,
			StageRelease,
			StageIsolation,
			StageEmergency,
			StageRestart,
		},
	})
	require.NoError(t, err)

	assert.True(t, called[StageControlPlane])
	assert.False(t, called[StageInventory])
	assert.False(t, called[StageArtifactName])
	assert.False(t, called[StageRelease])
	assert.False(t, called[StageIsolation])
	assert.False(t, called[StageEmergency])
	assert.False(t, called[StageRestart])
	assert.Equal(t, 1, report.Failed)
	assert.Equal(t, 6, report.Skipped)
	for _, result := range report.Results[1:] {
		assert.Equal(t, StageSkip, result.Status)
		assert.Equal(t, "stage_skipped", result.ErrorCode)
		assert.NotEmpty(t, result.RootCause)
	}
}

func TestHarness_IndependentBranchesContinue(t *testing.T) {
	t.Parallel()

	called := make(map[string]bool)
	stage := func(name string, err error) Stage {
		return NewStage(name, func(context.Context, *Fixture) error {
			called[name] = true
			return err
		})
	}

	h := NewHarness(
		StageSpec{Name: StageControlPlane, Stage: stage(StageControlPlane, nil)},
		StageSpec{Name: StageInventory, Stage: stage(StageInventory, errors.New("inventory failed"))},
		StageSpec{Name: StageArtifactName, Stage: stage(StageArtifactName, nil)},
		StageSpec{Name: StageRelease, Stage: stage(StageRelease, nil)},
		StageSpec{Name: StageIsolation, Stage: stage(StageIsolation, nil)},
		StageSpec{Name: StageEmergency, Stage: stage(StageEmergency, nil)},
		StageSpec{Name: StageRestart, Stage: stage(StageRestart, nil)},
	)

	report, err := h.Run(context.Background(), Scenario{
		SelectedStages: []string{
			StageControlPlane,
			StageInventory,
			StageArtifactName,
			StageRelease,
			StageIsolation,
			StageEmergency,
			StageRestart,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Failed)

	assert.True(t, called[StageControlPlane])
	assert.True(t, called[StageInventory])
	assert.True(t, called[StageArtifactName])
	assert.True(t, called[StageEmergency])
	assert.True(t, called[StageRestart])
	assert.False(t, called[StageRelease])
	assert.False(t, called[StageIsolation])
}

func TestHarness_ParallelRunsInventoryAndArtifact(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	stage := func(name string) Stage {
		return NewStage(name, func(context.Context, *Fixture) error {
			entered <- struct{}{}
			<-release
			return nil
		})
	}

	h := NewHarness(
		StageSpec{Name: StageControlPlane, Stage: NewStage(StageControlPlane, func(context.Context, *Fixture) error { return nil })},
		StageSpec{Name: StageInventory, Stage: stage(StageInventory)},
		StageSpec{Name: StageArtifactName, Stage: stage(StageArtifactName)},
	)
	h.Parallel = true

	done := make(chan Report, 1)
	errCh := make(chan error, 1)
	go func() {
		report, err := h.Run(context.Background(), Scenario{
			SelectedStages: []string{StageControlPlane, StageInventory, StageArtifactName},
		})
		done <- report
		errCh <- err
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("inventory/artifact did not start")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("inventory/artifact were not run in parallel")
	}
	close(release)

	select {
	case report := <-done:
		require.NoError(t, <-errCh)
		assert.Equal(t, 3, report.Passed)
	case <-time.After(time.Second):
		t.Fatal("parallel harness did not finish")
	}
}

func TestHarness_PanicBecomesStageFailure(t *testing.T) {
	t.Parallel()

	h := NewHarness(StageSpec{
		Name: StageControlPlane,
		Stage: NewStage(StageControlPlane, func(context.Context, *Fixture) error {
			panic("boom")
		}),
	})
	report, err := h.Run(context.Background(), Scenario{SelectedStages: []string{StageControlPlane}})
	require.Error(t, err)
	require.Len(t, report.Results, 1)
	assert.Equal(t, StageFail, report.Results[0].Status)
	var panicErr *PanicError
	assert.True(t, errors.As(report.Results[0].Error, &panicErr))
	var runErr *RunError
	assert.True(t, errors.As(err, &runErr))
	assert.Contains(t, report.Results[0].RootCause, "panic")
}
