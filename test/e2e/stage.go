// Package e2e provides a phased end-to-end test runner framework (REQ-066).
//
// Stages are small, context-aware units. The runner can execute the canonical
// stage graph or preserve the original linear Runner surface for callers that
// only need a short deterministic pipeline.
package e2e

import (
	"context"
	"errors"
	"time"
)

const (
	// Canonical stage names form the stable e2e-runner-surface vocabulary.
	StageControlPlane = "control-plane"
	StageInventory    = "inventory"
	StageArtifactName = "artifact"
	StageRelease      = "release"
	StageIsolation    = "isolation"
	StageEmergency    = "emergency"
	StageRestart      = "restart"
)

// CanonicalStages is the deterministic order used when a scenario does not
// provide a custom order. Callers should treat this compatibility slice as
// read-only; use CanonicalStageNames for a defensive copy.
var CanonicalStages = []string{
	StageControlPlane,
	StageInventory,
	StageArtifactName,
	StageRelease,
	StageIsolation,
	StageEmergency,
	StageRestart,
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

// ErrInvalidStageImplementation reports an unsupported stage implementation.
var ErrInvalidStageImplementation = errors.New("invalid stage implementation")

// ErrStageNotImplemented reports a canonical stage whose real implementation
// has not been wired yet. A stage without an implementation must fail closed
// rather than report a vacuous pass, so CI and local `make e2e-*` runs never
// go green on a stage body that does nothing (TASK-066 Step 8 fail-closed
// contract).
var ErrStageNotImplemented = errors.New("stage not implemented")

// NotImplementedError is the stable, serializable cause of a stage whose
// canonical implementation is not yet available.
type NotImplementedError struct {
	Stage string
}

func (e *NotImplementedError) Error() string {
	if e == nil || e.Stage == "" {
		return ErrStageNotImplemented.Error()
	}
	return "stage " + e.Stage + " not implemented"
}

// Is allows errors.Is(err, ErrStageNotImplemented) to classify the cause.
func (e *NotImplementedError) Is(target error) bool {
	return target == ErrStageNotImplemented
}

// Code returns the stable machine-readable error code surfaced in stage
// artifacts. It intentionally stays outside the live error-model table: it is
// an internal implementation-availability marker, never an environment fact.
func (e *NotImplementedError) Code() string { return "not_implemented" }

// UnimplementedStage returns a Stage that always fails closed.
func UnimplementedStage(name string) Stage {
	return NewStage(name, func(context.Context, *Fixture) error {
		return &NotImplementedError{Stage: name}
	})
}

// UnimplementedSpec returns a canonical graph node that fails closed until a
// real implementation replaces it.
func UnimplementedSpec(name string, dependencies ...string) StageSpec {
	return StageSpec{
		Name:         name,
		Stage:        UnimplementedStage(name),
		Dependencies: append([]string(nil), dependencies...),
	}
}

func (s *StageFunc) Name() string { return s.name }

func (s *StageFunc) Run(ctx context.Context, fixture *Fixture) error {
	if s == nil || s.fn == nil {
		return nil
	}
	return s.fn(ctx, fixture)
}

// StageSpec describes a stage in the dependency graph. Stage and Fn are
// alternate ways to provide the implementation; Stage takes precedence.
type StageSpec struct {
	Name         string
	Stage        Stage
	Fn           func(context.Context, *Fixture) error
	Dependencies []string
	// DependsOn is an accepted spelling kept for external scenario authors.
	DependsOn    []string
	Timeout      time.Duration
	ParallelSafe bool
}

// StageDefinition is an alias for callers that prefer the graph terminology.
type StageDefinition = StageSpec

// NewStageSpec adapts either a Stage or a stage function to a graph node.
func NewStageSpec(name string, implementation any, dependencies ...string) StageSpec {
	spec := StageSpec{Name: name, Dependencies: append([]string(nil), dependencies...)}
	switch value := implementation.(type) {
	case Stage:
		spec.Stage = value
	case func(context.Context, *Fixture) error:
		spec.Fn = value
	case nil:
	default:
		spec.Stage = NewStage(name, func(context.Context, *Fixture) error {
			return ErrInvalidStageImplementation
		})
	}
	return spec
}

// NewStageDefinition is a descriptive alias for NewStageSpec.
func NewStageDefinition(name string, implementation any, dependencies ...string) StageSpec {
	return NewStageSpec(name, implementation, dependencies...)
}

func (s StageSpec) stage() Stage {
	if s.Stage != nil {
		return s.Stage
	}
	if s.Fn != nil {
		return NewStage(s.Name, s.Fn)
	}
	return NewStage(s.Name, func(context.Context, *Fixture) error { return nil })
}

func (s StageSpec) dependencies() []string {
	if len(s.Dependencies) != 0 {
		return append([]string(nil), s.Dependencies...)
	}
	return append([]string(nil), s.DependsOn...)
}
