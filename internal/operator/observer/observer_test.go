package observer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserver_DeploymentReady(t *testing.T) {
	deployment := readyDeployment(4, "12")
	client := fake.NewSimpleClientset(deployment)
	observer := New(client)

	result, err := observer.Observe(t.Context(), deploymentRef(), 4, time.Second)

	require.NoError(t, err)
	assert.True(t, result.Ready)
	assert.Equal(t, int64(4), result.ObservedGeneration)
	assert.Equal(t, "12", result.ResourceVersion)
	require.Len(t, result.Conditions, 1)
	assert.Equal(t, "Available", result.Conditions[0].Type)
}

func TestObserver_DeploymentWaitsForObservedGeneration(t *testing.T) {
	deployment := readyDeployment(3, "12")
	deployment.Generation = 4
	client := fake.NewSimpleClientset(deployment)
	observer := New(client)

	result, err := observer.Observe(t.Context(), deploymentRef(), 4, 20*time.Millisecond)

	assert.ErrorIs(t, err, ErrRolloutTimeout)
	assert.False(t, result.Ready)
	assert.Equal(t, int64(3), result.ObservedGeneration)
}

func TestObserver_ReListsAfterWatchDisconnect(t *testing.T) {
	pending := readyDeployment(1, "1")
	pending.Generation = 2
	ready := readyDeployment(2, "2")

	client := fake.NewSimpleClientset(pending)
	firstWatch := watch.NewRaceFreeFake()
	secondWatch := watch.NewRaceFreeFake()

	var mu sync.Mutex
	listCalls := 0
	watchCalls := 0
	client.PrependReactor("list", "deployments", func(_ clienttesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		listCalls++
		if listCalls == 1 {
			return true, &appsv1.DeploymentList{
				ListMeta: metav1.ListMeta{ResourceVersion: "1"},
				Items:    []appsv1.Deployment{*pending.DeepCopy()},
			}, nil
		}
		return true, &appsv1.DeploymentList{
			ListMeta: metav1.ListMeta{ResourceVersion: "2"},
			Items:    []appsv1.Deployment{*ready.DeepCopy()},
		}, nil
	})
	client.PrependWatchReactor("deployments", func(_ clienttesting.Action) (bool, watch.Interface, error) {
		mu.Lock()
		defer mu.Unlock()
		watchCalls++
		if watchCalls == 1 {
			return true, firstWatch, nil
		}
		return true, secondWatch, nil
	})

	observer := New(client)
	resultCh := make(chan WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := observer.Observe(t.Context(), deploymentRef(), 2, time.Second)
		resultCh <- result
		errCh <- err
	}()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return watchCalls >= 1
	}, time.Second, time.Millisecond)
	firstWatch.Stop()

	select {
	case err := <-errCh:
		require.NoError(t, err)
		result := <-resultCh
		assert.True(t, result.Ready)
		assert.Equal(t, int64(2), result.ObservedGeneration)
	case <-time.After(time.Second):
		t.Fatal("observer did not recover after watch disconnect")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, listCalls, 2)
	assert.GreaterOrEqual(t, watchCalls, 1)
}

func TestObserver_RolloutTimeoutIncludesLastState(t *testing.T) {
	pending := readyDeployment(6, "21")
	pending.Generation = 7
	client := fake.NewSimpleClientset(pending)
	observer := New(client)

	result, err := observer.Observe(t.Context(), deploymentRef(), 7, 20*time.Millisecond)

	assert.ErrorIs(t, err, ErrRolloutTimeout)
	assert.Equal(t, int64(6), result.ObservedGeneration)
	assert.Equal(t, "21", result.ResourceVersion)
	var rolloutErr *RolloutError
	require.ErrorAs(t, err, &rolloutErr)
	assert.Equal(t, result, rolloutErr.Last)
}

func TestObserver_ContextCancelStopsWatch(t *testing.T) {
	deployment := readyDeployment(1, "1")
	deployment.Generation = 2
	client := fake.NewSimpleClientset(deployment)
	watcher := watch.NewRaceFreeFake()
	client.PrependWatchReactor("deployments", func(_ clienttesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	observer := New(client)
	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() {
		_, err := observer.Observe(ctx, deploymentRef(), 2, time.Second)
		errCh <- err
	}()

	require.Eventually(t, func() bool { return len(client.Actions()) >= 2 }, time.Second, time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, ErrCancelled)
	case <-time.After(time.Second):
		t.Fatal("observer did not stop after context cancellation")
	}
	require.Eventually(t, watcher.IsStopped, time.Second, time.Millisecond)
}

func TestStatefulSetReady(t *testing.T) {
	replicas := int32(3)
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default", Generation: 5, ResourceVersion: "15"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 5,
			ReadyReplicas:      3,
			UpdatedReplicas:    3,
			CurrentRevision:    "db-b",
			UpdateRevision:     "db-b",
		},
	}
	client := fake.NewSimpleClientset(statefulSet)
	observer := New(client)

	result, err := observer.Observe(t.Context(), ResourceRef{
		GVR:       StatefulSetGVR,
		Namespace: "default",
		Name:      "db",
	}, 5, time.Second)

	require.NoError(t, err)
	assert.True(t, result.Ready)
}

func TestDaemonSetReady(t *testing.T) {
	daemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default", Generation: 8, ResourceVersion: "22"},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration:     8,
			DesiredNumberScheduled: 4,
			UpdatedNumberScheduled: 4,
			NumberAvailable:        4,
			NumberUnavailable:      0,
		},
	}
	client := fake.NewSimpleClientset(daemonSet)
	observer := New(client)

	result, err := observer.Observe(t.Context(), ResourceRef{
		GVR:       DaemonSetGVR,
		Namespace: "default",
		Name:      "agent",
	}, 8, time.Second)

	require.NoError(t, err)
	assert.True(t, result.Ready)
}

func TestObserver_DeploymentUnavailable(t *testing.T) {
	deployment := readyDeployment(2, "5")
	deployment.Status.Conditions = []appsv1.DeploymentCondition{
		{
			Type:   appsv1.DeploymentProgressing,
			Status: corev1.ConditionFalse,
			Reason: "ProgressDeadlineExceeded",
		},
	}
	client := fake.NewSimpleClientset(deployment)
	observer := New(client)

	result, err := observer.Observe(t.Context(), deploymentRef(), 2, time.Second)

	assert.ErrorIs(t, err, ErrWorkloadUnavailable)
	assert.True(t, result.Failed)
}

func TestFakeObserver(t *testing.T) {
	ref := deploymentRef()
	tests := []struct {
		name      string
		response  FakeResponse
		cancel    bool
		wantReady bool
		wantErr   error
	}{
		{
			name:      "immediate ready",
			response:  FakeResponse{Behavior: FakeImmediateReady},
			wantReady: true,
		},
		{
			name:      "delayed ready",
			response:  FakeResponse{Behavior: FakeDelayedReady, Delay: time.Millisecond},
			wantReady: true,
		},
		{
			name:     "never ready times out",
			response: FakeResponse{Behavior: FakeNeverReady},
			wantErr:  ErrRolloutTimeout,
		},
		{
			name:     "injected error",
			response: FakeResponse{Behavior: FakeError, Err: ErrWatchDisconnected},
			wantErr:  ErrWatchDisconnected,
		},
		{
			name:     "context cancelled",
			response: FakeResponse{Behavior: FakeDelayedReady, Delay: time.Second},
			cancel:   true,
			wantErr:  ErrCancelled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeObserver := NewFake()
			fakeObserver.SetResponse(ref, tt.response)
			ctx, cancel := context.WithCancel(t.Context())
			if tt.cancel {
				cancel()
			} else {
				defer cancel()
			}

			result, err := fakeObserver.Observe(ctx, ref, 1, 10*time.Millisecond)

			assert.Equal(t, tt.wantReady, result.Ready)
			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, tt.wantErr)
			}
			assert.Equal(t, []ResourceRef{ref}, fakeObserver.Calls())
		})
	}
}

func TestRolloutError_UnwrapsKind(t *testing.T) {
	err := &RolloutError{Kind: ErrRolloutTimeout, Err: context.DeadlineExceeded}
	assert.True(t, errors.Is(err, ErrRolloutTimeout))
	assert.Equal(t, context.DeadlineExceeded, err.Cause())
}

func deploymentRef() ResourceRef {
	return ResourceRef{GVR: DeploymentGVR, Namespace: "default", Name: "web"}
}

func readyDeployment(generation int64, resourceVersion string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "web",
			Namespace:       "default",
			Generation:      generation,
			ResourceVersion: resourceVersion,
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: generation,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}
}
