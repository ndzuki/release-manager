package stages

import (
	"context"
	"fmt"
	"strings"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

// CodeRestartTriggerFailed identifies a restart that could not be applied to a
// bound control-plane Deployment.
const CodeRestartTriggerFailed = "restart_trigger_failed"

// DeploymentRestart is the rollout marker of one restarted Deployment. The
// marker is the restart annotation value and the generation observed right
// after the patch, so convergence is asserted without a fixed sleep.
type DeploymentRestart struct {
	Namespace  string
	Name       string
	Marker     string
	Generation int64
}

// RestartProbe is the consumer-side seam for the restart barrier. The client-go
// implementation lives in restart_probe.go; tests inject deterministic fakes.
//
// The probe MUST NOT delete Pods or shell out to kubectl: it patches a restart
// annotation on the bound Deployments and waits for their own rollout to
// converge (AC-066-03/28/30/39).
type RestartProbe interface {
	// Trigger restarts every bound Deployment and returns one marker each.
	Trigger(ctx context.Context) ([]DeploymentRestart, error)
	// AwaitRollout waits until every restarted Deployment converged on its
	// marker with all replicas ready.
	AwaitRollout(ctx context.Context, restarts []DeploymentRestart) error
}

// ControlPlaneWaiter observes control-plane recovery after a restart. It is a
// separate seam because it talks HTTP/Connect rather than the Kubernetes API.
type ControlPlaneWaiter interface {
	// AwaitServices waits until every declared control-plane endpoint is ready.
	AwaitServices(ctx context.Context) error
	// AwaitOperatorSession waits until an operator session is online again.
	AwaitOperatorSession(ctx context.Context) error
}

// RestartStage patches the bound control-plane Deployments and asserts the
// full recovery barrier: rollout convergence, service readiness, and an online
// operator session. It registers no compensation because the barrier itself is
// the restoration: a partially converged restart is a failed stage, never a
// silently accepted state.
type RestartStage struct {
	probe  RestartProbe
	waiter ControlPlaneWaiter
	guard  func(context.Context) error

	restarts []DeploymentRestart
}

var _ e2e.Stage = (*RestartStage)(nil)

// NewRestartStage creates the restart barrier stage.
func NewRestartStage(probe RestartProbe, waiter ControlPlaneWaiter) *RestartStage {
	return &RestartStage{probe: probe, waiter: waiter}
}

// NewRestart is a concise alias for NewRestartStage.
func NewRestart(probe RestartProbe, waiter ControlPlaneWaiter) *RestartStage {
	return NewRestartStage(probe, waiter)
}

// WithControlPlaneGuard installs the guard that runs before the restart when
// the control-plane stage was not selected in the same run (D-026 D8). The
// guard is expected to perform the six-service readiness, environment
// consistency, and operator-session checks.
func (s *RestartStage) WithControlPlaneGuard(guard func(context.Context) error) *RestartStage {
	if s != nil {
		s.guard = guard
	}
	return s
}

// Name implements e2e.Stage.
func (s *RestartStage) Name() string { return "restart" }

// Run implements e2e.Stage.
func (s *RestartStage) Run(ctx context.Context, _ *e2e.Fixture) error {
	if s == nil || s.probe == nil {
		return newStageError(CodeSnapshotNotFound, "restart", "restart probe unavailable")
	}
	if s.waiter == nil {
		return newStageError(CodeSnapshotNotFound, "restart", "control-plane waiter unavailable")
	}
	if s.guard != nil {
		if err := s.guard(ctx); err != nil {
			return newStageError(CodeEnvironmentUnhealthy, "restart", "control-plane guard failed")
		}
	}

	restarts, err := s.probe.Trigger(ctx)
	if err != nil {
		return newStageError(CodeRestartTriggerFailed, "restart", "restart trigger failed")
	}
	if len(restarts) == 0 {
		return newStageError(CodeRestartTriggerFailed, "restart", "no restart targets were patched")
	}
	for _, restart := range restarts {
		if strings.TrimSpace(restart.Name) == "" || strings.TrimSpace(restart.Marker) == "" {
			return newStageError(CodeRestartTriggerFailed, "restart", "restart marker missing")
		}
	}

	if err := s.probe.AwaitRollout(ctx, restarts); err != nil {
		return newStageError(CodeRestartTimeout, "restart", "deployment rollout did not converge")
	}
	if err := s.waiter.AwaitServices(ctx); err != nil {
		return newStageError(CodeRestartTimeout, "restart", "control-plane services did not become ready")
	}
	if err := s.waiter.AwaitOperatorSession(ctx); err != nil {
		return newStageError(CodeRestartTimeout, "restart", "operator session did not come back online")
	}

	s.restarts = restarts
	return nil
}

// Restarts returns the markers of the last successful restart.
func (s *RestartStage) Restarts() []DeploymentRestart {
	if s == nil {
		return nil
	}
	return append([]DeploymentRestart(nil), s.restarts...)
}

// RestartMarkerFor returns the marker recorded for one deployment, if present.
func (s *RestartStage) RestartMarkerFor(name string) (DeploymentRestart, bool) {
	if s == nil {
		return DeploymentRestart{}, false
	}
	for _, restart := range s.restarts {
		if restart.Name == name {
			return restart, true
		}
	}
	return DeploymentRestart{}, false
}

// FormatRestartSummary renders a stable, non-sensitive summary for artifacts.
func FormatRestartSummary(restarts []DeploymentRestart) string {
	parts := make([]string, 0, len(restarts))
	for _, restart := range restarts {
		parts = append(parts, fmt.Sprintf("%s/%s@%s", restart.Namespace, restart.Name, restart.Marker))
	}
	return strings.Join(parts, ",")
}
