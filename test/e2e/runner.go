package e2e

import (
	"context"
	"fmt"
	"time"
)

// Runner executes E2E stages sequentially with fixture propagation and
// skip-on-failure semantics (AC-066-01). A per-stage timeout ensures
// that a stuck stage does not block the entire pipeline (AC-066-04).
type Runner struct {
	Stages      []Stage
	StageTimeout time.Duration // timeout per stage; 0 means no deadline

	// SkipOnFail controls whether subsequent stages are skipped when
	// a previous stage fails. Default true.
	SkipOnFail bool
}

// NewRunner creates a Runner with sensible defaults.
func NewRunner(stages ...Stage) *Runner {
	return &Runner{
		Stages:      stages,
		StageTimeout: 5 * time.Minute,
		SkipOnFail:  true,
	}
}

// RunResult aggregates the results of all stages.
type RunResult struct {
	Results      []StageResult
	TotalElapsed time.Duration
	// Skipped counts stages that were skipped due to prior failure.
	Skipped int
	// Failed counts stages that returned an error.
	Failed int
}

// AllPassed reports whether every stage passed (no failures, no skips).
func (r *RunResult) AllPassed() bool { return r.Failed == 0 && r.Skipped == 0 }

// Run executes all stages in order, propagating the fixture through
// the pipeline.
func (r *Runner) Run(ctx context.Context, fixture *Fixture) *RunResult {
	start := time.Now()
	result := &RunResult{}
	prevFailed := false

	for _, stage := range r.Stages {
		if prevFailed && r.SkipOnFail {
			result.Results = append(result.Results, StageResult{
				Name:   stage.Name(),
				Status: StageSkip,
				Cause:  "previous stage failed",
			})
			result.Skipped++
			continue
		}

		stageRes := r.runStage(ctx, stage, fixture)
		result.Results = append(result.Results, stageRes)

		if stageRes.Status == StageFail {
			result.Failed++
			prevFailed = true
		}
	}

	result.TotalElapsed = time.Since(start)
	return result
}

// runStage executes a single stage with its own deadline.
func (r *Runner) runStage(ctx context.Context, stage Stage, fixture *Fixture) StageResult {
	stageStart := time.Now()
	res := StageResult{Name: stage.Name()}

	stageCtx := ctx
	var cancel context.CancelFunc
	if r.StageTimeout > 0 {
		stageCtx, cancel = context.WithTimeout(ctx, r.StageTimeout)
		defer cancel()
	}

	if err := stage.Run(stageCtx, fixture); err != nil {
		res.Status = StageFail
		res.Error = err
		if stageCtx.Err() != nil && ctx.Err() == nil {
			// Stage timed out; parent context is still healthy.
			res.Cause = fmt.Sprintf("stage timeout (%s): %v", r.StageTimeout, err)
		} else {
			res.Cause = err.Error()
		}
	} else {
		res.Status = StagePass
	}

	res.Duration = time.Since(stageStart)
	return res
}
