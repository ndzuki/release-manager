package observer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserver_DeploymentReady(t *testing.T) {
	deployment := readyDeployment(4, "12")
	deployment.UID = "deployment-uid"
	client := fake.NewSimpleClientset(deployment)
	observer := New(client)

	result, err := observer.Observe(t.Context(), deploymentRef(), 4, time.Second)

	require.NoError(t, err)
	assert.True(t, result.Ready)
	assert.False(t, result.Failed)
	assert.Equal(t, deployment.UID, result.ResourceUID)
	assert.Equal(t, deployment.Generation, result.Generation)
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

func TestObserver_DeploymentRequiresAllReadyCounters(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*appsv1.Deployment)
	}{
		{
			name: "updated replicas lag",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Status.UpdatedReplicas = 0
			},
		},
		{
			name: "available replicas lag",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Status.AvailableReplicas = 0
			},
		},
		{
			name: "unavailable replicas remain",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Status.UnavailableReplicas = 1
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployment := readyDeployment(4, "12")
			tt.mutate(deployment)

			result, err := New(fake.NewSimpleClientset(deployment)).Observe(t.Context(), deploymentRef(), 4, 20*time.Millisecond)

			assert.ErrorIs(t, err, ErrRolloutTimeout)
			assert.False(t, result.Ready)
			assertRolloutLast(t, result, err)
		})
	}
}

func TestObserver_ValidatesInputWithoutAPIRequests(t *testing.T) {
	cancelledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	unsupportedRef := ResourceRef{
		GVR:       schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"},
		Namespace: "default",
		Name:      "widget",
	}
	tests := []struct {
		name               string
		ctx                context.Context
		ref                ResourceRef
		expectedGeneration int64
		timeout            time.Duration
		wantKind           error
		wantCode           ErrorCode
		wantMessage        string
		wantCause          error
	}{
		{
			name:               "nil context",
			ctx:                nil,
			ref:                deploymentRef(),
			expectedGeneration: 1,
			timeout:            time.Second,
			wantKind:           ErrInvalidArgument,
			wantCode:           ErrorCodeInvalidArgument,
			wantMessage:        "invalid rollout watch argument: context: must not be nil",
		},
		{
			name:               "cancelled context",
			ctx:                cancelledCtx,
			ref:                deploymentRef(),
			expectedGeneration: 1,
			timeout:            time.Second,
			wantKind:           ErrCancelled,
			wantCode:           ErrorCodeCancelled,
			wantMessage:        "rollout watch cancelled: default/web",
			wantCause:          context.Canceled,
		},
		{
			name:               "empty namespace",
			ctx:                t.Context(),
			ref:                ResourceRef{GVR: DeploymentGVR, Name: "web"},
			expectedGeneration: 1,
			timeout:            time.Second,
			wantKind:           ErrInvalidArgument,
			wantCode:           ErrorCodeInvalidArgument,
			wantMessage:        "invalid rollout watch argument: namespace: must not be empty",
		},
		{
			name:               "empty name",
			ctx:                t.Context(),
			ref:                ResourceRef{GVR: DeploymentGVR, Namespace: "default"},
			expectedGeneration: 1,
			timeout:            time.Second,
			wantKind:           ErrInvalidArgument,
			wantCode:           ErrorCodeInvalidArgument,
			wantMessage:        "invalid rollout watch argument: name: must not be empty",
		},
		{
			name:               "invalid apps generation",
			ctx:                t.Context(),
			ref:                deploymentRef(),
			expectedGeneration: 0,
			timeout:            time.Second,
			wantKind:           ErrInvalidArgument,
			wantCode:           ErrorCodeInvalidArgument,
			wantMessage:        "invalid rollout watch argument: expectedGeneration: must be greater than zero for apps workloads",
		},
		{
			name:               "invalid job generation",
			ctx:                t.Context(),
			ref:                jobRef(),
			expectedGeneration: 1,
			timeout:            time.Second,
			wantKind:           ErrInvalidArgument,
			wantCode:           ErrorCodeInvalidArgument,
			wantMessage:        "invalid rollout watch argument: expectedGeneration: must be zero for jobs",
		},
		{
			name:               "invalid timeout",
			ctx:                t.Context(),
			ref:                deploymentRef(),
			expectedGeneration: 1,
			timeout:            0,
			wantKind:           ErrInvalidArgument,
			wantCode:           ErrorCodeInvalidArgument,
			wantMessage:        "invalid rollout watch argument: timeout: must be greater than zero",
		},
		{
			name:               "unsupported resource",
			ctx:                t.Context(),
			ref:                unsupportedRef,
			expectedGeneration: 0,
			timeout:            time.Second,
			wantKind:           ErrUnsupportedResource,
			wantCode:           ErrorCodeUnsupportedResource,
			wantMessage:        "unsupported rollout resource: example.io/v1, Resource=widgets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()

			result, err := New(client).Observe(tt.ctx, tt.ref, tt.expectedGeneration, tt.timeout)

			assert.ErrorIs(t, err, tt.wantKind)
			assert.Equal(t, tt.wantCode, rolloutErrorCode(t, err))
			assert.EqualError(t, err, tt.wantMessage)
			assert.Equal(t, tt.ref, result.Resource)
			assert.Empty(t, client.Actions())
			assertRolloutLast(t, result, err)
			if tt.wantCause != nil {
				var rolloutErr *RolloutError
				require.ErrorAs(t, err, &rolloutErr)
				assert.ErrorIs(t, rolloutErr.Cause(), tt.wantCause)
			}
		})
	}
}

func TestObserver_MissingWorkloadReturnsUnavailableWithoutWatch(t *testing.T) {
	client := fake.NewSimpleClientset()

	result, err := New(client).Observe(t.Context(), deploymentRef(), 1, time.Second)

	assert.ErrorIs(t, err, ErrWorkloadUnavailable)
	assert.Equal(t, ErrorCodeWorkloadUnavailable, rolloutErrorCode(t, err))
	assert.Equal(t, deploymentRef(), result.Resource)
	assert.Empty(t, result.ResourceUID)
	assert.Zero(t, result.Generation)
	assert.Zero(t, result.ObservedGeneration)
	assert.Empty(t, result.ResourceVersion)
	assert.False(t, result.Ready)
	assert.False(t, result.Failed)
	assertRolloutLast(t, result, err)
	require.Len(t, client.Actions(), 1)
	assert.Equal(t, "list", client.Actions()[0].GetVerb())
}

func TestObserver_ReListsAfterWatchDisconnect(t *testing.T) {
	pending := readyDeployment(1, "1")
	pending.Generation = 2
	relisted := pending.DeepCopy()
	relisted.ResourceVersion = "2"
	ready := readyDeployment(2, "3")
	client := fake.NewSimpleClientset(pending)
	firstWatch := watch.NewRaceFreeFake()
	secondWatch := watch.NewRaceFreeFake()

	var mu sync.Mutex
	listCalls := 0
	watchVersions := make([]string, 0, 2)
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
			Items:    []appsv1.Deployment{*relisted},
		}, nil
	})
	client.PrependWatchReactor("deployments", func(action clienttesting.Action) (bool, watch.Interface, error) {
		watchAction, ok := action.(clienttesting.WatchAction)
		require.True(t, ok)
		mu.Lock()
		defer mu.Unlock()
		watchVersions = append(watchVersions, watchAction.GetWatchRestrictions().ResourceVersion)
		if len(watchVersions) == 1 {
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
		return len(watchVersions) >= 1
	}, time.Second, time.Millisecond)
	firstWatch.Stop()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(watchVersions) >= 2
	}, time.Second, time.Millisecond)
	secondWatch.Modify(ready)

	select {
	case err := <-errCh:
		require.NoError(t, err)
		result := <-resultCh
		assert.True(t, result.Ready)
		assert.Equal(t, int64(2), result.ObservedGeneration)
		assert.Equal(t, "3", result.ResourceVersion)
	case <-time.After(time.Second):
		t.Fatal("observer did not recover after watch disconnect")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, listCalls, 2)
	assert.Equal(t, []string{"1", "2"}, watchVersions)
}

func TestObserver_IgnoresBookmarksUntilReady(t *testing.T) {
	pending := readyDeployment(1, "1")
	pending.Generation = 2
	pending.UID = "deployment-uid"
	ready := readyDeployment(2, "3")
	ready.UID = pending.UID
	client := fake.NewSimpleClientset(pending)
	watcher := watch.NewRaceFreeFake()
	client.PrependWatchReactor("deployments", func(_ clienttesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	resultCh := make(chan WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := New(client).Observe(t.Context(), deploymentRef(), 2, time.Second)
		resultCh <- result
		errCh <- err
	}()

	require.Eventually(t, func() bool { return len(client.Actions()) >= 2 }, time.Second, time.Millisecond)
	watcher.Action(watch.Bookmark, &metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{ResourceVersion: "2"},
	})
	select {
	case err := <-errCh:
		t.Fatalf("observer returned after bookmark: %v", err)
	default:
	}
	watcher.Modify(ready)

	select {
	case err := <-errCh:
		require.NoError(t, err)
		result := <-resultCh
		assert.True(t, result.Ready)
		assert.Equal(t, ready.ResourceVersion, result.ResourceVersion)
	case <-time.After(time.Second):
		t.Fatal("observer did not continue after bookmark")
	}
}

func TestObserver_ListResourceVersionExpiredDoesNotLeakInternalCode(t *testing.T) {
	pending := readyDeployment(1, "1")
	pending.Generation = 2
	client := fake.NewSimpleClientset(pending)
	cause := apierrors.NewResourceExpired("expired list resource version")
	listCalls := 0
	client.PrependReactor("list", "deployments", func(_ clienttesting.Action) (bool, runtime.Object, error) {
		listCalls++
		return true, nil, cause
	})

	result, err := New(client).Observe(t.Context(), deploymentRef(), 2, time.Second)

	assert.ErrorIs(t, err, ErrWatchDisconnected)
	assert.Equal(t, ErrorCodeWatchDisconnected, rolloutErrorCode(t, err))
	var rolloutErr *RolloutError
	require.ErrorAs(t, err, &rolloutErr)
	assert.Same(t, cause, rolloutErr.Cause())
	assert.Equal(t, 1, listCalls)
	assertRolloutLast(t, result, err)
}

func TestObserver_ReListRejectsReplacementUID(t *testing.T) {
	pending := readyDeployment(1, "1")
	pending.Generation = 2
	pending.UID = "original-uid"
	replacement := readyDeployment(2, "2")
	replacement.UID = "replacement-uid"
	client := fake.NewSimpleClientset(pending)
	firstWatch := watch.NewRaceFreeFake()

	var mu sync.Mutex
	listCalls := 0
	client.PrependReactor("list", "deployments", func(_ clienttesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		listCalls++
		if listCalls == 1 {
			return true, &appsv1.DeploymentList{ListMeta: metav1.ListMeta{ResourceVersion: "1"}, Items: []appsv1.Deployment{*pending.DeepCopy()}}, nil
		}
		return true, &appsv1.DeploymentList{ListMeta: metav1.ListMeta{ResourceVersion: "2"}, Items: []appsv1.Deployment{*replacement.DeepCopy()}}, nil
	})
	client.PrependWatchReactor("deployments", func(_ clienttesting.Action) (bool, watch.Interface, error) {
		return true, firstWatch, nil
	})

	resultCh := make(chan WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := New(client).Observe(t.Context(), deploymentRef(), 2, time.Second)
		resultCh <- result
		errCh <- err
	}()

	require.Eventually(t, func() bool { return len(client.Actions()) >= 2 }, time.Second, time.Millisecond)
	firstWatch.Stop()

	select {
	case err := <-errCh:
		result := <-resultCh
		assert.ErrorIs(t, err, ErrWorkloadUnavailable)
		assert.Equal(t, pending.UID, result.ResourceUID)
		assert.Equal(t, ErrorCodeWorkloadUnavailable, rolloutErrorCode(t, err))
		assertRolloutLast(t, result, err)
	case <-time.After(time.Second):
		t.Fatal("observer followed replacement UID")
	}
}

func TestObserver_WatchRejectsReplacementUID(t *testing.T) {
	pending := readyDeployment(1, "1")
	pending.Generation = 2
	pending.UID = "original-uid"
	replacement := readyDeployment(2, "2")
	replacement.UID = "replacement-uid"
	client := fake.NewSimpleClientset(pending)
	watcher := watch.NewRaceFreeFake()
	client.PrependWatchReactor("deployments", func(_ clienttesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	resultCh := make(chan WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := New(client).Observe(t.Context(), deploymentRef(), 2, time.Second)
		resultCh <- result
		errCh <- err
	}()

	require.Eventually(t, func() bool { return len(client.Actions()) >= 2 }, time.Second, time.Millisecond)
	watcher.Modify(replacement)

	select {
	case err := <-errCh:
		result := <-resultCh
		assert.ErrorIs(t, err, ErrWorkloadUnavailable)
		assert.Equal(t, pending.UID, result.ResourceUID)
		assertRolloutLast(t, result, err)
	case <-time.After(time.Second):
		t.Fatal("observer followed replacement UID")
	}
}

func TestObserver_DeletedPreservesLockedUID(t *testing.T) {
	pending := readyDeployment(1, "1")
	pending.Generation = 2
	pending.UID = "original-uid"
	client := fake.NewSimpleClientset(pending)
	watcher := watch.NewRaceFreeFake()
	client.PrependWatchReactor("deployments", func(_ clienttesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	resultCh := make(chan WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := New(client).Observe(t.Context(), deploymentRef(), 2, time.Second)
		resultCh <- result
		errCh <- err
	}()

	require.Eventually(t, func() bool { return len(client.Actions()) >= 2 }, time.Second, time.Millisecond)
	watcher.Delete(pending.DeepCopy())

	select {
	case err := <-errCh:
		result := <-resultCh
		assert.ErrorIs(t, err, ErrWorkloadUnavailable)
		assert.Equal(t, pending.UID, result.ResourceUID)
		assertRolloutLast(t, result, err)
		assert.True(t, watcher.IsStopped())
	case <-time.After(time.Second):
		t.Fatal("observer did not stop after deletion")
	}
}

func TestObserver_ParentCancellationWinsTimeout(t *testing.T) {
	for range 20 {
		pending := readyDeployment(1, "1")
		pending.UID = "deployment-uid"
		pending.Generation = 2
		client := fake.NewSimpleClientset(pending)
		watcher := watch.NewRaceFreeFake()
		client.PrependWatchReactor("deployments", func(_ clienttesting.Action) (bool, watch.Interface, error) {
			return true, watcher, nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		resultCh := make(chan WatchResult, 1)
		errCh := make(chan error, 1)
		go func() {
			result, err := New(client).Observe(ctx, deploymentRef(), 2, time.Second)
			resultCh <- result
			errCh <- err
		}()

		require.Eventually(t, func() bool { return len(client.Actions()) >= 2 }, time.Second, time.Millisecond)
		cancel()

		select {
		case err := <-errCh:
			result := <-resultCh
			assert.ErrorIs(t, err, ErrCancelled)
			assert.Equal(t, ErrorCodeCancelled, rolloutErrorCode(t, err))
			assertRolloutLast(t, result, err)
			assert.Equal(t, pending.UID, result.ResourceUID)
			assert.Equal(t, pending.Generation, result.Generation)
			assert.Equal(t, pending.ResourceVersion, result.ResourceVersion)
			assert.True(t, watcher.IsStopped())
		case <-time.After(time.Second):
			t.Fatal("observer did not stop after parent cancellation")
		}
	}
}

func TestObserver_ParentCancellationWinsAtDeadlineBoundary(t *testing.T) {
	for range 100 {
		synctest.Test(t, func(t *testing.T) {
			pending := readyDeployment(1, "1")
			pending.Generation = 2
			client := fake.NewSimpleClientset(pending)
			watcher := watch.NewRaceFreeFake()
			client.PrependWatchReactor("deployments", func(_ clienttesting.Action) (bool, watch.Interface, error) {
				return true, watcher, nil
			})
			ctx, cancel := context.WithCancel(t.Context())
			resultCh := make(chan WatchResult, 1)
			errCh := make(chan error, 1)
			go func() {
				result, err := New(client).Observe(ctx, deploymentRef(), 2, time.Second)
				resultCh <- result
				errCh <- err
			}()

			synctest.Wait()
			time.Sleep(time.Second - time.Nanosecond)
			cancel()
			time.Sleep(time.Nanosecond)
			synctest.Wait()

			err := <-errCh
			result := <-resultCh
			assert.ErrorIs(t, err, ErrCancelled)
			assert.Equal(t, ErrorCodeCancelled, rolloutErrorCode(t, err))
			assertRolloutLast(t, result, err)
			assert.True(t, watcher.IsStopped())
		})
	}
}

func TestObserver_ConcurrentCallsAreIsolated(t *testing.T) {
	pending := readyDeployment(1, "1")
	pending.Generation = 2
	pending.UID = "deployment-uid"
	ready := readyDeployment(2, "2")
	ready.UID = pending.UID

	client := fake.NewSimpleClientset(pending.DeepCopy())
	firstWatcher := watch.NewRaceFreeFake()
	secondWatcher := watch.NewRaceFreeFake()
	var watchCalls atomic.Int64
	client.PrependWatchReactor("deployments", func(_ clienttesting.Action) (bool, watch.Interface, error) {
		if watchCalls.Add(1) == 1 {
			return true, firstWatcher, nil
		}
		return true, secondWatcher, nil
	})
	observer := New(client)

	ctx, cancel := context.WithCancel(t.Context())
	firstResultCh := make(chan WatchResult, 1)
	firstErrCh := make(chan error, 1)
	secondResultCh := make(chan WatchResult, 1)
	secondErrCh := make(chan error, 1)
	go func() {
		result, err := observer.Observe(ctx, deploymentRef(), 2, time.Second)
		firstResultCh <- result
		firstErrCh <- err
	}()
	require.Eventually(t, func() bool { return watchCalls.Load() == 1 }, time.Second, time.Millisecond)
	go func() {
		result, err := observer.Observe(t.Context(), deploymentRef(), 2, time.Second)
		secondResultCh <- result
		secondErrCh <- err
	}()

	require.Eventually(t, func() bool { return watchCalls.Load() == 2 }, time.Second, time.Millisecond)
	cancel()

	firstErr := <-firstErrCh
	firstResult := <-firstResultCh
	assert.ErrorIs(t, firstErr, ErrCancelled)
	assert.False(t, firstResult.Ready)

	secondWatcher.Modify(ready)
	secondErr := <-secondErrCh
	secondResult := <-secondResultCh
	require.NoError(t, secondErr)
	assert.True(t, secondResult.Ready)
	assert.Equal(t, pending.UID, secondResult.ResourceUID)
	assert.True(t, firstWatcher.IsStopped())
	assert.True(t, secondWatcher.IsStopped())
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
	statefulSet := readyStatefulSet()
	client := fake.NewSimpleClientset(statefulSet)
	observer := New(client)

	result, err := observer.Observe(t.Context(), statefulSetRef(), 5, time.Second)

	require.NoError(t, err)
	assert.True(t, result.Ready)
	assert.False(t, result.Failed)
	assert.Equal(t, statefulSet.UID, result.ResourceUID)
	assert.Equal(t, statefulSet.Generation, result.Generation)
	assert.Equal(t, statefulSet.Status.ObservedGeneration, result.ObservedGeneration)
	assert.Equal(t, statefulSet.ResourceVersion, result.ResourceVersion)
}

func TestStatefulSetRequiresAllReadySignals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*appsv1.StatefulSet)
	}{
		{name: "observed generation lags", mutate: func(statefulSet *appsv1.StatefulSet) {
			statefulSet.Status.ObservedGeneration--
		}},
		{name: "updated replicas lag", mutate: func(statefulSet *appsv1.StatefulSet) {
			statefulSet.Status.UpdatedReplicas--
		}},
		{name: "ready replicas lag", mutate: func(statefulSet *appsv1.StatefulSet) {
			statefulSet.Status.ReadyReplicas--
		}},
		{name: "revision mismatch", mutate: func(statefulSet *appsv1.StatefulSet) {
			statefulSet.Status.CurrentRevision = "db-a"
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			statefulSet := readyStatefulSet()
			tt.mutate(statefulSet)

			result, err := New(fake.NewSimpleClientset(statefulSet)).Observe(t.Context(), statefulSetRef(), 5, 20*time.Millisecond)

			assert.ErrorIs(t, err, ErrRolloutTimeout)
			assert.False(t, result.Ready)
			assert.False(t, result.Failed)
			assertRolloutLast(t, result, err)
		})
	}
}

func TestDaemonSetReady(t *testing.T) {
	daemonSet := readyDaemonSet()
	client := fake.NewSimpleClientset(daemonSet)
	observer := New(client)

	result, err := observer.Observe(t.Context(), daemonSetRef(), 8, time.Second)

	require.NoError(t, err)
	assert.True(t, result.Ready)
	assert.False(t, result.Failed)
	assert.Equal(t, daemonSet.UID, result.ResourceUID)
	assert.Equal(t, daemonSet.Generation, result.Generation)
	assert.Equal(t, daemonSet.Status.ObservedGeneration, result.ObservedGeneration)
	assert.Equal(t, daemonSet.ResourceVersion, result.ResourceVersion)
}

func TestDaemonSetRequiresAllReadySignals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*appsv1.DaemonSet)
	}{
		{name: "observed generation lags", mutate: func(daemonSet *appsv1.DaemonSet) {
			daemonSet.Status.ObservedGeneration--
		}},
		{name: "updated scheduled count lags", mutate: func(daemonSet *appsv1.DaemonSet) {
			daemonSet.Status.UpdatedNumberScheduled--
		}},
		{name: "available count lags", mutate: func(daemonSet *appsv1.DaemonSet) {
			daemonSet.Status.NumberAvailable--
		}},
		{name: "unavailable count remains", mutate: func(daemonSet *appsv1.DaemonSet) {
			daemonSet.Status.NumberUnavailable = 1
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemonSet := readyDaemonSet()
			tt.mutate(daemonSet)

			result, err := New(fake.NewSimpleClientset(daemonSet)).Observe(t.Context(), daemonSetRef(), 8, 20*time.Millisecond)

			assert.ErrorIs(t, err, ErrRolloutTimeout)
			assert.False(t, result.Ready)
			assert.False(t, result.Failed)
			assertRolloutLast(t, result, err)
		})
	}
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
	assertRolloutLast(t, result, err)
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

func TestObserver_WatchErrorPreservesCause(t *testing.T) {
	pending := readyDeployment(1, "1")
	pending.Generation = 2
	cause := errors.New("watch request failed")
	client := fake.NewSimpleClientset(pending)
	client.PrependWatchReactor("deployments", func(_ clienttesting.Action) (bool, watch.Interface, error) {
		return true, nil, cause
	})

	result, err := New(client).Observe(t.Context(), deploymentRef(), 2, time.Second)

	assert.ErrorIs(t, err, ErrWatchDisconnected)
	var rolloutErr *RolloutError
	require.ErrorAs(t, err, &rolloutErr)
	assert.ErrorIs(t, rolloutErr.Cause(), cause)
	assertRolloutLast(t, result, err)
}

func TestRolloutError_CodeReturnsStableValues(t *testing.T) {
	tests := []struct {
		name string
		kind error
		want ErrorCode
	}{
		{name: "invalid argument", kind: ErrInvalidArgument, want: ErrorCodeInvalidArgument},
		{name: "unsupported resource", kind: ErrUnsupportedResource, want: ErrorCodeUnsupportedResource},
		{name: "workload unavailable", kind: ErrWorkloadUnavailable, want: ErrorCodeWorkloadUnavailable},
		{name: "rollout timeout", kind: ErrRolloutTimeout, want: ErrorCodeRolloutTimeout},
		{name: "cancelled", kind: ErrCancelled, want: ErrorCodeCancelled},
		{name: "watch disconnected", kind: ErrWatchDisconnected, want: ErrorCodeWatchDisconnected},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &RolloutError{Kind: tt.kind}

			assert.Equal(t, tt.want, err.Code())
		})
	}
}

func deploymentRef() ResourceRef {
	return ResourceRef{GVR: DeploymentGVR, Namespace: "default", Name: "web"}
}

func statefulSetRef() ResourceRef {
	return ResourceRef{GVR: StatefulSetGVR, Namespace: "default", Name: "db"}
}

func daemonSetRef() ResourceRef {
	return ResourceRef{GVR: DaemonSetGVR, Namespace: "default", Name: "agent"}
}

func readyStatefulSet() *appsv1.StatefulSet {
	replicas := int32(3)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default", UID: "statefulset-uid", Generation: 5, ResourceVersion: "15"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 5,
			ReadyReplicas:      3,
			UpdatedReplicas:    3,
			CurrentRevision:    "db-b",
			UpdateRevision:     "db-b",
		},
	}
}

func readyDaemonSet() *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default", UID: "daemonset-uid", Generation: 8, ResourceVersion: "22"},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration:     8,
			DesiredNumberScheduled: 4,
			UpdatedNumberScheduled: 4,
			NumberAvailable:        4,
			NumberUnavailable:      0,
		},
	}
}

func readyDeployment(generation int64, resourceVersion string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "web",
			Namespace:       "default",
			Generation:      generation,
			ResourceVersion: resourceVersion,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration:  generation,
			UpdatedReplicas:     replicas,
			AvailableReplicas:   replicas,
			UnavailableReplicas: 0,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}
}
