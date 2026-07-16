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
