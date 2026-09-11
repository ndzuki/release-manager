// Package stages contains the read-only adapters used by the E2E stage runner.
package stages

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

const (
	// CodeEnvironmentUnhealthy identifies a failed readiness or environment guard.
	CodeEnvironmentUnhealthy = "environment_unhealthy"
	// CodeFixtureStale identifies an identity mismatch against the seed manifest.
	CodeFixtureStale = "fixture_stale"
	// CodeSnapshotNotFound identifies an unavailable read-only observation.
	CodeSnapshotNotFound = "snapshot_not_found"
)

var (
	// ErrEnvironmentUnhealthy is the stable root cause for control-plane failures.
	ErrEnvironmentUnhealthy = errors.New(CodeEnvironmentUnhealthy)
	// ErrFixtureStale is the stable root cause for seed identity drift.
	ErrFixtureStale = errors.New(CodeFixtureStale)
	// ErrSnapshotNotFound is the stable root cause for missing observations.
	ErrSnapshotNotFound = errors.New(CodeSnapshotNotFound)

	// ErrOperationRejected is the stable root cause for a refused write.
	ErrOperationRejected = errors.New(CodeOperationRejected)
	// ErrOperationFailed is the stable root cause for a non-succeeded operation.
	ErrOperationFailed = errors.New(CodeOperationFailed)
	// ErrReleaseBusy is the stable root cause for a foreign active operation.
	ErrReleaseBusy = errors.New(CodeReleaseBusy)
	// ErrCleanupTimeout is the stable root cause for a bounded cleanup miss.
	ErrCleanupTimeout = errors.New(CodeCleanupTimeout)
	// ErrEffectUnknown is the stable root cause for an unobserved emergency effect.
	ErrEffectUnknown = errors.New(CodeEffectUnknown)
	// ErrRestartTimeout is the stable root cause for an unconverged restart.
	ErrRestartTimeout = errors.New(CodeRestartTimeout)
	// ErrRestartTriggerFailed is the stable root cause for a failed restart patch.
	ErrRestartTriggerFailed = errors.New(CodeRestartTriggerFailed)
	// ErrCrossTargetChanged is the stable root cause for a non-target that was
	// perturbed by a write the stage did not intend for it.
	ErrCrossTargetChanged = errors.New(CodeCrossTargetChanged)
)

// StageError is a sanitized, machine-readable stage failure.
//
// Detail is intentionally limited to public entity names and expected/actual
// values. Injected clients can return sensitive transport errors, but those
// errors are not copied into this type.
type StageError struct {
	Code      string `json:"code"`
	Component string `json:"component,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

func (e *StageError) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{e.Code}
	if e.Component != "" {
		parts = append(parts, e.Component)
	}
	if e.Detail != "" {
		parts = append(parts, e.Detail)
	}
	return strings.Join(parts, ": ")
}

// ErrorCode exposes the stable machine-readable failure code.
//
// The runner reads the code through an accessor rather than the Code field
// because a Go method cannot share its name with a field, and the runner has to
// recover the code from an error returned across the stage boundary. Without
// this accessor every live stage failure degrades to the generic "stage_failed"
// artifact code and the specific code survives only inside root_cause.
func (e *StageError) ErrorCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func (e *StageError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.Code {
	case CodeEnvironmentUnhealthy:
		return ErrEnvironmentUnhealthy
	case CodeFixtureStale:
		return ErrFixtureStale
	case CodeSnapshotNotFound:
		return ErrSnapshotNotFound
	case CodeOperationRejected:
		return ErrOperationRejected
	case CodeOperationFailed:
		return ErrOperationFailed
	case CodeReleaseBusy:
		return ErrReleaseBusy
	case CodeCleanupTimeout:
		return ErrCleanupTimeout
	case CodeEffectUnknown:
		return ErrEffectUnknown
	case CodeRestartTimeout:
		return ErrRestartTimeout
	case CodeRestartTriggerFailed:
		return ErrRestartTriggerFailed
	case CodeCrossTargetChanged:
		return ErrCrossTargetChanged
	default:
		return nil
	}
}

func newStageError(code, component, detail string) error {
	return &StageError{Code: code, Component: component, Detail: detail}
}

// EnvironmentObservation is the public metadata returned by /environment.
type EnvironmentObservation struct {
	Service       string
	Environment   string
	EnvironmentID string
	Production    bool
}

// ServiceObservation is the read-only health and environment result for one
// control-plane endpoint.
type ServiceObservation struct {
	Name        string
	Healthy     bool
	Ready       bool
	Environment EnvironmentObservation
}

// OperatorSessionObservation is the safe subset of an active operator session
// needed by the control-plane stage.
type OperatorSessionObservation struct {
	SessionID     string
	OperatorID    string
	Status        string
	LastHeartbeat string
	Online        bool
}

// ControlPlaneObservation is the complete read-only control-plane result.
type ControlPlaneObservation struct {
	Services        []ServiceObservation
	OperatorSession OperatorSessionObservation
}

// ControlPlaneObserver is the consumer-side seam for readiness and environment
// clients. Implementations may use Connect, HTTP, or deterministic fakes.
type ControlPlaneObserver interface {
	ObserveControlPlane(context.Context) (ControlPlaneObservation, error)
}

// ControlPlaneStage validates readiness, environment consistency, and operator
// liveness without changing any service or cluster state.
type ControlPlaneStage struct {
	observer         ControlPlaneObserver
	expectedServices []string
	observation      ControlPlaneObservation
}

var _ e2e.Stage = (*ControlPlaneStage)(nil)

// NewControlPlaneStage creates a read-only control-plane stage. When service
// names are supplied, the observation must contain exactly those names; when
// omitted, six distinct public services are required.
func NewControlPlaneStage(observer ControlPlaneObserver, expectedServices ...string) *ControlPlaneStage {
	services := append([]string(nil), expectedServices...)
	sort.Strings(services)
	return &ControlPlaneStage{observer: observer, expectedServices: services}
}

// NewControlPlane is a concise alias for NewControlPlaneStage.
func NewControlPlane(observer ControlPlaneObserver, expectedServices ...string) *ControlPlaneStage {
	return NewControlPlaneStage(observer, expectedServices...)
}

// Name implements e2e.Stage.
func (s *ControlPlaneStage) Name() string { return "control-plane" }

// Run implements e2e.Stage.
func (s *ControlPlaneStage) Run(ctx context.Context, _ *e2e.Fixture) error {
	if s == nil || s.observer == nil {
		return newStageError(CodeSnapshotNotFound, "control-plane", "control-plane observation unavailable")
	}
	observation, err := s.observer.ObserveControlPlane(ctx)
	if err != nil {
		return newStageError(CodeEnvironmentUnhealthy, "control-plane", "control-plane observation failed")
	}
	if err := validateControlPlane(observation, s.expectedServices); err != nil {
		return err
	}
	s.observation = observation
	return nil
}

// Observation returns the last validated observation. The returned slices are
// copied so callers cannot mutate stage-owned state.
func (s *ControlPlaneStage) Observation() ControlPlaneObservation {
	if s == nil {
		return ControlPlaneObservation{}
	}
	observation := s.observation
	observation.Services = append([]ServiceObservation(nil), observation.Services...)
	return observation
}

func validateControlPlane(observation ControlPlaneObservation, expectedServices []string) error {
	if len(observation.Services) == 0 {
		return newStageError(CodeSnapshotNotFound, "control-plane", "service observations missing")
	}
	if len(expectedServices) > 0 {
		if len(observation.Services) != len(expectedServices) {
			return newStageError(CodeEnvironmentUnhealthy, "control-plane", fmt.Sprintf("service count expected %d got %d", len(expectedServices), len(observation.Services)))
		}
		expected := make(map[string]struct{}, len(expectedServices))
		for _, name := range expectedServices {
			expected[name] = struct{}{}
		}
		for _, service := range observation.Services {
			if _, ok := expected[service.Name]; !ok {
				return newStageError(CodeEnvironmentUnhealthy, service.Name, "unexpected service observation")
			}
		}
	} else if len(observation.Services) != 6 {
		return newStageError(CodeEnvironmentUnhealthy, "control-plane", fmt.Sprintf("service count expected 6 got %d", len(observation.Services)))
	}

	seen := make(map[string]struct{}, len(observation.Services))
	var reference EnvironmentObservation
	for index, service := range observation.Services {
		if strings.TrimSpace(service.Name) == "" {
			return newStageError(CodeEnvironmentUnhealthy, "control-plane", "service name missing")
		}
		if _, ok := seen[service.Name]; ok {
			return newStageError(CodeEnvironmentUnhealthy, service.Name, "duplicate service observation")
		}
		seen[service.Name] = struct{}{}
		if !service.Healthy || !service.Ready {
			return newStageError(CodeEnvironmentUnhealthy, service.Name, "service is not healthy and ready")
		}
		if strings.TrimSpace(service.Environment.EnvironmentID) == "" || strings.TrimSpace(service.Environment.Environment) == "" {
			return newStageError(CodeEnvironmentUnhealthy, service.Name, "environment metadata missing")
		}
		if service.Environment.Production {
			return newStageError(CodeEnvironmentUnhealthy, service.Name, "production environment rejected")
		}
		if index == 0 {
			reference = service.Environment
			continue
		}
		if service.Environment.Environment != reference.Environment || service.Environment.EnvironmentID != reference.EnvironmentID {
			return newStageError(CodeEnvironmentUnhealthy, service.Name, "environment metadata inconsistent")
		}
	}

	session := observation.OperatorSession
	if strings.TrimSpace(session.SessionID) == "" || strings.TrimSpace(session.OperatorID) == "" {
		return newStageError(CodeEnvironmentUnhealthy, "operator-session", "active session identity missing")
	}
	if !session.Online && !strings.EqualFold(strings.TrimSpace(session.Status), "online") {
		return newStageError(CodeEnvironmentUnhealthy, "operator-session", "operator session is offline")
	}
	return nil
}
