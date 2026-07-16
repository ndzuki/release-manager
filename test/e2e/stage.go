// Package e2e provides a phased end-to-end test runner framework (REQ-066).
//
// Stages are executed sequentially; a failed stage allows subsequent stages
// to opt into skip mode. Timeout handling preserves completed stage results.
package e2e

import (
	"context"
	"fmt"
	"time"
)

// StageStatus is the outcome of a single stage execution.
type StageStatus string

const (
	StagePass StageStatus = "pass"
	StageFail StageStatus = "fail"
	StageSkip StageStatus = "skip"
)

// StageResult holds the outcome of running one stage.
type StageResult struct {
	Name     string
	Status   StageStatus
	Error    error
	Duration time.Duration
	// Cause provides a human-readable reason when Status is fail or skip.
	Cause string
}

func (r StageResult) String() string {
	switch r.Status {
	case StagePass:
		return fmt.Sprintf("%s: pass (%s)", r.Name, r.Duration.Round(time.Millisecond))
	case StageFail:
		return fmt.Sprintf("%s: fail — %s (%s)", r.Name, r.Cause, r.Duration.Round(time.Millisecond))
	case StageSkip:
		return fmt.Sprintf("%s: skip — %s", r.Name, r.Cause)
	default:
		return fmt.Sprintf("%s: %s", r.Name, r.Status)
	}
}

// Stage is a single phase in an E2E pipeline.
//
// Run receives the runner context (which carries a deadline) and a fixture
// snapshot from the previous stage. It returns an error to signal failure;
// a nil error means pass.
type Stage interface {
	Name() string
	Run(ctx context.Context, fixture *Fixture) error
}

// StageFunc adapts a plain function to the Stage interface.
type StageFunc struct {
	name string
	fn   func(ctx context.Context, fixture *Fixture) error
}

// NewStage creates a Stage from a name and function.
func NewStage(name string, fn func(ctx context.Context, fixture *Fixture) error) Stage {
	return &StageFunc{name: name, fn: fn}
}

func (s *StageFunc) Name() string { return s.name }

func (s *StageFunc) Run(ctx context.Context, fixture *Fixture) error {
	return s.fn(ctx, fixture)
}
