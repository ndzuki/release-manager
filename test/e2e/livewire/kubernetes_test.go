package livewire

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// observerFixture builds the read-only observer over a fake typed clientset.
func observerFixture(t *testing.T, objects ...runtime.Object) *ReplicaObserver {
	t.Helper()
	observer, err := NewReplicaObserver(fake.NewSimpleClientset(objects...))
	if err != nil {
		t.Fatalf("NewReplicaObserver() error = %v", err)
	}
	return observer
}

func deployment(namespace, name string, replicas, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Status:     appsv1.DeploymentStatus{Replicas: replicas, ReadyReplicas: ready},
	}
}

func statefulSet(namespace, name string, replicas, ready int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Status:     appsv1.StatefulSetStatus{Replicas: replicas, ReadyReplicas: ready},
	}
}

func TestObserveReplicasReadsDeploymentStatus(t *testing.T) {
	t.Parallel()

	observer := observerFixture(t, deployment("release-fixture", "app", 5, 4))
	observation, err := observer.ObserveReplicas(context.Background(), "release-fixture", "app")
	if err != nil {
		t.Fatalf("ObserveReplicas() error = %v", err)
	}
	if observation.Replicas != 5 || observation.Ready != 4 {
		t.Fatalf("ObserveReplicas() = %+v, want replicas 5 and ready 4", observation)
	}
	// readyReplicas is reported separately because a scale-up that never
	// becomes ready must not read as a successful applied effect.
	if observation.Workload != "Deployment/release-fixture/app" {
		t.Fatalf("Workload = %q", observation.Workload)
	}
}

func TestObserveReplicasFallsBackToOtherKinds(t *testing.T) {
	t.Parallel()

	observer := observerFixture(t, statefulSet("release-fixture", "db", 2, 2))
	observation, err := observer.ObserveReplicas(context.Background(), "release-fixture", "db")
	if err != nil {
		t.Fatalf("ObserveReplicas() error = %v", err)
	}
	if observation.Replicas != 2 || observation.Ready != 2 {
		t.Fatalf("ObserveReplicas() = %+v, want replicas 2 and ready 2", observation)
	}
	if observation.Workload != "StatefulSet/release-fixture/db" {
		t.Fatalf("Workload = %q", observation.Workload)
	}
}

func TestObserveReplicasFailsClosedWhenAbsent(t *testing.T) {
	t.Parallel()

	observer := observerFixture(t)
	// A missing workload must be an error, never a zero-replica observation:
	// "0 observed" would otherwise look like a successful scale-to-zero.
	if _, err := observer.ObserveReplicas(context.Background(), "release-fixture", "app"); err == nil {
		t.Fatal("ObserveReplicas() error = nil, want a fail-closed error")
	}
}

func TestObserveReplicasFailsClosedWhenAmbiguous(t *testing.T) {
	t.Parallel()

	observer := observerFixture(t,
		deployment("release-fixture", "app", 3, 3),
		statefulSet("release-fixture", "app", 3, 3),
	)
	_, err := observer.ObserveReplicas(context.Background(), "release-fixture", "app")
	if err == nil {
		t.Fatal("ObserveReplicas() error = nil, want an error for an ambiguous workload")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ObserveReplicas() error = %v, want an ambiguity error", err)
	}
}

func TestObserveReplicasPropagatesReadFailure(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset(deployment("release-fixture", "app", 3, 3))
	// A permission failure is not an absent workload; treating it as one would
	// report "not found" for a namespace the runner simply cannot read.
	client.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "app", errors.New("denied"))
	})
	observer, err := NewReplicaObserver(client)
	if err != nil {
		t.Fatalf("NewReplicaObserver() error = %v", err)
	}
	if _, err := observer.ObserveReplicas(context.Background(), "release-fixture", "app"); err == nil {
		t.Fatal("ObserveReplicas() error = nil, want the read failure to surface")
	}
}

func TestObserveReplicasRequiresCoordinates(t *testing.T) {
	t.Parallel()

	observer := observerFixture(t)
	if _, err := observer.ObserveReplicas(context.Background(), " ", "app"); err == nil {
		t.Fatal("ObserveReplicas() error = nil, want an error for a missing namespace")
	}
	if _, err := observer.ObserveReplicas(context.Background(), "ns", ""); err == nil {
		t.Fatal("ObserveReplicas() error = nil, want an error for a missing workload name")
	}
}

func TestNewReplicaObserverRejectsNilClient(t *testing.T) {
	t.Parallel()

	if _, err := NewReplicaObserver(nil); err == nil {
		t.Fatal("NewReplicaObserver(nil) error = nil, want an error")
	}
}

func TestNewKubernetesClientRejectsMissingKubeconfig(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	cfg := *h.cfg
	cfg.K3d.Kubeconfig = ""
	if _, err := NewKubernetesClient(&cfg); err == nil {
		t.Fatal("NewKubernetesClient() error = nil, want an error for an empty kubeconfig")
	}
	if _, err := NewKubernetesClient(nil); err == nil {
		t.Fatal("NewKubernetesClient(nil) error = nil, want an error")
	}
}
