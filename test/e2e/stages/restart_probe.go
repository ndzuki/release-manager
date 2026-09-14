package stages

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// RestartAnnotationKey is the annotation patched to trigger a Deployment
// rollout. It matches the well-known kubectl rollout restart key so operators
// reading the cluster see a familiar marker.
const RestartAnnotationKey = "kubectl.kubernetes.io/restartedAt"

// RestartTargets are the statically bound control-plane Deployments the
// restart stage may patch. The RBAC role is expected to name exactly these
// resources (no label-based discovery), so an empty or incomplete binding is
// fail-closed.
type RestartTargets struct {
	Namespace   string
	Deployments []string
}

// Validate fails closed when the restart binding is incomplete.
func (t RestartTargets) Validate() error {
	if strings.TrimSpace(t.Namespace) == "" {
		return fmt.Errorf("restart targets: namespace missing")
	}
	if len(t.Deployments) == 0 {
		return fmt.Errorf("restart targets: no deployments bound")
	}
	seen := make(map[string]struct{}, len(t.Deployments))
	for _, name := range t.Deployments {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return fmt.Errorf("restart targets: empty deployment name")
		}
		if _, ok := seen[trimmed]; ok {
			return fmt.Errorf("restart targets: duplicate deployment %q", trimmed)
		}
		seen[trimmed] = struct{}{}
	}
	return nil
}

// KubernetesRestartProbe patches the bound Deployments through client-go and
// waits for their rollout to converge. It performs no Pod deletion and never
// shells out: the only write is the restart annotation on a statically bound
// Deployment.
type KubernetesRestartProbe struct {
	client       kubernetes.Interface
	targets      RestartTargets
	pollInterval time.Duration
	now          func() time.Time
}

var _ RestartProbe = (*KubernetesRestartProbe)(nil)

// NewKubernetesRestartProbe creates a client-go restart probe.
func NewKubernetesRestartProbe(client kubernetes.Interface, targets RestartTargets) (*KubernetesRestartProbe, error) {
	if client == nil {
		return nil, fmt.Errorf("restart probe: nil kubernetes client")
	}
	if err := targets.Validate(); err != nil {
		return nil, err
	}
	return &KubernetesRestartProbe{
		client:       client,
		targets:      targets,
		pollInterval: time.Second,
		now:          time.Now,
	}, nil
}

// WithPollInterval overrides the rollout poll cadence. It exists for tests that
// need a fast barrier; production keeps the one second default.
func (p *KubernetesRestartProbe) WithPollInterval(interval time.Duration) *KubernetesRestartProbe {
	if p != nil && interval > 0 {
		p.pollInterval = interval
	}
	return p
}

// WithClock overrides the marker clock for deterministic tests.
func (p *KubernetesRestartProbe) WithClock(now func() time.Time) *KubernetesRestartProbe {
	if p != nil && now != nil {
		p.now = now
	}
	return p
}

// Trigger patches the restart annotation on every bound Deployment and records
// the generation observed after the patch as the convergence target.
func (p *KubernetesRestartProbe) Trigger(ctx context.Context) ([]DeploymentRestart, error) {
	if p == nil || p.client == nil {
		return nil, fmt.Errorf("restart probe: unavailable")
	}
	if err := p.targets.Validate(); err != nil {
		return nil, err
	}
	marker := p.now().UTC().Format(time.RFC3339)
	body, patchType := PatchForRestart(marker)
	restarts := make([]DeploymentRestart, 0, len(p.targets.Deployments))
	for _, name := range p.targets.Deployments {
		updated, err := p.client.AppsV1().Deployments(p.targets.Namespace).Patch(ctx, name, patchType, body, metav1.PatchOptions{})
		if err != nil {
			return nil, fmt.Errorf("restart probe: patch deployment %s: %w", name, err)
		}
		restarts = append(restarts, DeploymentRestart{
			Namespace:  p.targets.Namespace,
			Name:       name,
			Marker:     marker,
			Generation: updated.Generation,
		})
	}
	return restarts, nil
}

// AwaitRollout waits until every Deployment converged on its marker. Each
// Deployment is polled independently; the first failure aborts the barrier so
// a partial restart is never reported as success. A read failure is treated as
// a hard failure: the barrier must not report a converged rollout it could not
// actually observe.
func (p *KubernetesRestartProbe) AwaitRollout(ctx context.Context, restarts []DeploymentRestart) error {
	if p == nil || p.client == nil {
		return fmt.Errorf("restart probe: unavailable")
	}
	for _, restart := range restarts {
		restart := restart
		pollErr := wait.PollUntilContextCancel(ctx, p.pollInterval, true, func(condCtx context.Context) (bool, error) {
			deployment, getErr := p.client.AppsV1().Deployments(restart.Namespace).Get(condCtx, restart.Name, metav1.GetOptions{})
			if getErr != nil {
				return false, getErr
			}
			return deploymentConverged(deployment, restart), nil
		})
		if pollErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("restart probe: deployment %s did not converge: %w", restart.Name, pollErr)
		}
	}
	return nil
}

// deploymentConverged reports whether a Deployment has fully rolled out onto
// the restart marker: the annotation is present, the controller observed the
// newest generation, and every desired replica is updated, ready and available.
func deploymentConverged(deployment *appsv1.Deployment, restart DeploymentRestart) bool {
	if deployment == nil {
		return false
	}
	if deployment.Annotations[RestartAnnotationKey] != restart.Marker {
		return false
	}
	if deployment.Generation < restart.Generation {
		return false
	}
	if deployment.Status.ObservedGeneration < deployment.Generation {
		return false
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if deployment.Status.UpdatedReplicas != desired {
		return false
	}
	if deployment.Status.ReadyReplicas != desired {
		return false
	}
	if deployment.Status.AvailableReplicas != desired {
		return false
	}
	return deployment.Status.UnavailableReplicas == 0
}

// PatchForRestart builds the strategic-merge patch body used to trigger a
// rollout. It sets the restart annotation on the pod template (which is what
// actually rolls the Deployment and bumps its generation) and mirrors it on the
// Deployment's own metadata so the barrier can read the marker back without
// re-deriving it. It is exported so live wiring and tests share one shape.
func PatchForRestart(marker string) ([]byte, types.PatchType) {
	body := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q}},"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`,
		RestartAnnotationKey, marker, RestartAnnotationKey, marker)
	return []byte(body), types.StrategicMergePatchType
}
