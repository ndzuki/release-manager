package stages

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

type restartFake struct {
	restarts   []DeploymentRestart
	triggerErr error
	rolloutErr error

	triggerCalls int
	rolloutCalls int
	observed     []DeploymentRestart
}

func (f *restartFake) Trigger(context.Context) ([]DeploymentRestart, error) {
	f.triggerCalls++
	if f.triggerErr != nil {
		return nil, f.triggerErr
	}
	return f.restarts, nil
}

func (f *restartFake) AwaitRollout(_ context.Context, restarts []DeploymentRestart) error {
	f.rolloutCalls++
	f.observed = append([]DeploymentRestart(nil), restarts...)
	return f.rolloutErr
}

type waiterFake struct {
	servicesErr error
	sessionErr  error
	services    int
	sessions    int
}

func (w *waiterFake) AwaitServices(context.Context) error {
	w.services++
	return w.servicesErr
}

func (w *waiterFake) AwaitOperatorSession(context.Context) error {
	w.sessions++
	return w.sessionErr
}

func healthyRestarts() []DeploymentRestart {
	return []DeploymentRestart{
		{Namespace: "k3d-test", Name: "release-auth", Marker: "m1", Generation: 4},
		{Namespace: "k3d-test", Name: "release-orchestrator", Marker: "m1", Generation: 7},
	}
}

func TestRestartStageRun(t *testing.T) {
	t.Parallel()

	base := func() (*restartFake, *waiterFake) {
		return &restartFake{restarts: healthyRestarts()}, &waiterFake{}
	}

	tests := []struct {
		name     string
		mutate   func(*restartFake, *waiterFake)
		guard    func(context.Context) error
		want     error
		wantTrig int
		wantWait int
	}{
		{name: "pass", wantTrig: 1, wantWait: 1},
		{
			name:     "trigger failed",
			mutate:   func(p *restartFake, _ *waiterFake) { p.triggerErr = errors.New("private detail") },
			want:     ErrRestartTriggerFailed,
			wantTrig: 1,
		},
		{
			name:     "no restart targets patched",
			mutate:   func(p *restartFake, _ *waiterFake) { p.restarts = nil },
			want:     ErrRestartTriggerFailed,
			wantTrig: 1,
		},
		{
			name: "marker missing",
			mutate: func(p *restartFake, _ *waiterFake) {
				p.restarts = []DeploymentRestart{{Namespace: "k3d-test", Name: "release-auth"}}
			},
			want:     ErrRestartTriggerFailed,
			wantTrig: 1,
		},
		{
			name:     "rollout not converged",
			mutate:   func(p *restartFake, _ *waiterFake) { p.rolloutErr = errors.New("private detail") },
			want:     ErrRestartTimeout,
			wantTrig: 1,
			wantWait: 1,
		},
		{
			name:     "services not ready",
			mutate:   func(_ *restartFake, w *waiterFake) { w.servicesErr = errors.New("private detail") },
			want:     ErrRestartTimeout,
			wantTrig: 1,
			wantWait: 1,
		},
		{
			name:     "session not online",
			mutate:   func(_ *restartFake, w *waiterFake) { w.sessionErr = errors.New("private detail") },
			want:     ErrRestartTimeout,
			wantTrig: 1,
			wantWait: 1,
		},
		{
			name:     "guard failed",
			guard:    func(context.Context) error { return errors.New("private detail") },
			want:     ErrEnvironmentUnhealthy,
			wantTrig: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			probe, waiter := base()
			if test.mutate != nil {
				test.mutate(probe, waiter)
			}
			stage := NewRestartStage(probe, waiter)
			if test.guard != nil {
				stage.WithControlPlaneGuard(test.guard)
			}

			err := stage.Run(context.Background(), nil)
			if test.want == nil {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				if got := len(stage.Restarts()); got != 2 {
					t.Fatalf("Restarts() = %d, want 2", got)
				}
				if _, ok := stage.RestartMarkerFor("release-orchestrator"); !ok {
					t.Fatal("RestartMarkerFor(release-orchestrator) missing")
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want errors.Is(..., %v)", err, test.want)
			}
			if probe.triggerCalls != test.wantTrig {
				t.Fatalf("trigger calls = %d, want %d", probe.triggerCalls, test.wantTrig)
			}
			if probe.rolloutCalls != test.wantWait {
				t.Fatalf("rollout calls = %d, want %d", probe.rolloutCalls, test.wantWait)
			}
		})
	}
}

func TestRestartStageGuardRunsOnlyWhenInstalled(t *testing.T) {
	t.Parallel()

	var guarded bool
	probe := &restartFake{restarts: healthyRestarts()}
	waiter := &waiterFake{}
	stage := NewRestartStage(probe, waiter).WithControlPlaneGuard(func(context.Context) error {
		guarded = true
		return nil
	})
	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !guarded {
		t.Fatal("control-plane guard must run when installed")
	}
}

func TestRestartStageMissingDependencies(t *testing.T) {
	t.Parallel()

	if err := NewRestartStage(nil, &waiterFake{}).Run(context.Background(), nil); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("nil probe error = %v, want ErrSnapshotNotFound", err)
	}
	if err := NewRestartStage(&restartFake{}, nil).Run(context.Background(), nil); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("nil waiter error = %v, want ErrSnapshotNotFound", err)
	}
}

func TestRestartStageRegistersNoCompensation(t *testing.T) {
	t.Parallel()

	// Restart convergence is its own restoration; the stage must not add a
	// second, racing compensation driver.
	registry := e2e.NewCompensationRegistry()
	stage := NewRestartStage(&restartFake{restarts: healthyRestarts()}, &waiterFake{})
	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if registry.Len() != 0 {
		t.Fatalf("registry.Len() = %d, want 0", registry.Len())
	}
}

func TestFormatRestartSummary(t *testing.T) {
	t.Parallel()

	got := FormatRestartSummary(healthyRestarts())
	want := "k3d-test/release-auth@m1,k3d-test/release-orchestrator@m1"
	if got != want {
		t.Fatalf("FormatRestartSummary() = %q, want %q", got, want)
	}
}

func TestRestartTargetsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		targets RestartTargets
		wantErr bool
	}{
		{name: "valid", targets: RestartTargets{Namespace: "k3d-test", Deployments: []string{"a", "b"}}},
		{name: "missing namespace", targets: RestartTargets{Deployments: []string{"a"}}, wantErr: true},
		{name: "no deployments", targets: RestartTargets{Namespace: "k3d-test"}, wantErr: true},
		{name: "empty name", targets: RestartTargets{Namespace: "k3d-test", Deployments: []string{" "}}, wantErr: true},
		{name: "duplicate", targets: RestartTargets{Namespace: "k3d-test", Deployments: []string{"a", "a"}}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.targets.Validate()
			if test.wantErr && err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestNewKubernetesRestartProbeRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	if _, err := NewKubernetesRestartProbe(nil, RestartTargets{Namespace: "ns", Deployments: []string{"a"}}); err == nil {
		t.Fatal("nil client must be rejected")
	}
	if _, err := NewKubernetesRestartProbe(fake.NewSimpleClientset(), RestartTargets{}); err == nil {
		t.Fatal("empty targets must be rejected")
	}
}

func TestKubernetesRestartProbeTriggerAndRollout(t *testing.T) {
	t.Parallel()

	namespace := "k3d-test"
	names := []string{"release-auth", "release-orchestrator"}
	objects := make([]runtime.Object, 0, len(names))
	for _, name := range names {
		// ObservedGeneration lags the generation, so the barrier can never
		// report convergence against the fake clientset's static status.
		deployment := convergedDeployment(namespace, name, 1, 0)
		deployment.Generation = 1
		deployment.Status.ObservedGeneration = 0
		objects = append(objects, deployment)
	}
	client := fake.NewSimpleClientset(objects...)

	probe, err := NewKubernetesRestartProbe(client, RestartTargets{Namespace: namespace, Deployments: names})
	if err != nil {
		t.Fatalf("NewKubernetesRestartProbe() error = %v", err)
	}
	probe.WithPollInterval(5 * time.Millisecond)

	marker := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	probe.WithClock(func() time.Time { return marker })

	restarts, err := probe.Trigger(context.Background())
	if err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}
	if len(restarts) != 2 {
		t.Fatalf("Trigger() returned %d restarts, want 2", len(restarts))
	}
	for _, restart := range restarts {
		if restart.Marker != marker.Format(time.RFC3339) {
			t.Fatalf("marker = %q, want the injected clock value", restart.Marker)
		}
		deployment, getErr := client.AppsV1().Deployments(namespace).Get(context.Background(), restart.Name, metav1.GetOptions{})
		if getErr != nil {
			t.Fatalf("get deployment %s: %v", restart.Name, getErr)
		}
		if deployment.Annotations[RestartAnnotationKey] != restart.Marker {
			t.Fatalf("annotation on %s = %q, want %q", restart.Name, deployment.Annotations[RestartAnnotationKey], restart.Marker)
		}
		if restart.Generation != deployment.Generation {
			t.Fatalf("generation = %d, want %d", restart.Generation, deployment.Generation)
		}
	}

	// The fake clientset keeps the pre-patch status, so convergence must fail
	// inside a short deadline rather than hang.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := probe.AwaitRollout(ctx, restarts); err == nil {
		t.Fatal("AwaitRollout() = nil, want a deadline error while status lags the generation")
	}
}

func TestKubernetesRestartProbeAwaitRolloutConverges(t *testing.T) {
	t.Parallel()

	namespace := "k3d-test"
	marker := "2026-09-10T12:00:00Z"
	deployment := convergedDeployment(namespace, "release-auth", 1, 0)
	deployment.Generation = 2
	deployment.Status.ObservedGeneration = 2
	deployment.Annotations = map[string]string{RestartAnnotationKey: marker}
	client := fake.NewSimpleClientset(deployment)

	probe, err := NewKubernetesRestartProbe(client, RestartTargets{Namespace: namespace, Deployments: []string{"release-auth"}})
	if err != nil {
		t.Fatalf("NewKubernetesRestartProbe() error = %v", err)
	}
	probe.WithPollInterval(5 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := probe.AwaitRollout(ctx, []DeploymentRestart{{Namespace: namespace, Name: "release-auth", Marker: marker, Generation: 2}}); err != nil {
		t.Fatalf("AwaitRollout() error = %v", err)
	}
}

func TestKubernetesRestartProbeTriggerMissingDeployment(t *testing.T) {
	t.Parallel()

	probe, err := NewKubernetesRestartProbe(fake.NewSimpleClientset(), RestartTargets{Namespace: "k3d-test", Deployments: []string{"missing"}})
	if err != nil {
		t.Fatalf("NewKubernetesRestartProbe() error = %v", err)
	}
	if _, err := probe.Trigger(context.Background()); err == nil {
		t.Fatal("Trigger() = nil, want an error for an unbound deployment")
	}
}

func TestDeploymentConverged(t *testing.T) {
	t.Parallel()

	marker := "m1"
	converged := func() *appsv1.Deployment {
		deployment := convergedDeployment("ns", "app", 1, 0)
		deployment.Generation = 3
		deployment.Status.ObservedGeneration = 3
		deployment.Annotations = map[string]string{RestartAnnotationKey: marker}
		return deployment
	}
	restart := DeploymentRestart{Namespace: "ns", Name: "app", Marker: marker, Generation: 3}

	tests := []struct {
		name   string
		mutate func(*appsv1.Deployment)
		want   bool
	}{
		{name: "converged", want: true},
		{name: "nil", mutate: nil, want: true},
		{name: "marker mismatch", mutate: func(d *appsv1.Deployment) { d.Annotations[RestartAnnotationKey] = "other" }},
		{name: "observed generation behind", mutate: func(d *appsv1.Deployment) { d.Status.ObservedGeneration = 2 }},
		{name: "updated replicas behind", mutate: func(d *appsv1.Deployment) { d.Status.UpdatedReplicas = 0 }},
		{name: "ready replicas behind", mutate: func(d *appsv1.Deployment) { d.Status.ReadyReplicas = 0 }},
		{name: "available replicas behind", mutate: func(d *appsv1.Deployment) { d.Status.AvailableReplicas = 0 }},
		{name: "unavailable replicas present", mutate: func(d *appsv1.Deployment) { d.Status.UnavailableReplicas = 1 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.name == "nil" {
				if deploymentConverged(nil, restart) {
					t.Fatal("nil deployment must not be converged")
				}
				return
			}
			deployment := converged()
			if test.mutate != nil {
				test.mutate(deployment)
			}
			if got := deploymentConverged(deployment, restart); got != test.want {
				t.Fatalf("deploymentConverged() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPatchForRestartShape(t *testing.T) {
	t.Parallel()

	body, patchType := PatchForRestart("m1")
	if patchType != "application/strategic-merge-patch+json" {
		t.Fatalf("patch type = %q", patchType)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("patch body is not valid JSON: %v", err)
	}
	if !strings.Contains(string(body), RestartAnnotationKey) {
		t.Fatalf("patch body %s lacks the restart annotation key", body)
	}
}

func convergedDeployment(namespace, name string, desired, unavailable int32) *appsv1.Deployment {
	replicas := desired
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			Replicas:            desired,
			UpdatedReplicas:     desired,
			ReadyReplicas:       desired,
			AvailableReplicas:   desired,
			UnavailableReplicas: unavailable,
			ObservedGeneration:  1,
		},
	}
}
