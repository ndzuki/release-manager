package observer

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

type RolloutObserver interface {
	Observe(ctx context.Context, ref ResourceRef, expectedGeneration int64, timeout time.Duration) (WatchResult, error)
}

type Observer struct {
	client kubernetes.Interface
}

func New(client kubernetes.Interface) *Observer {
	return &Observer{client: client}
}

type resourceSource struct {
	current func(context.Context, metav1.ListOptions) (runtime.Object, string, error)
	watch   func(context.Context, metav1.ListOptions) (watch.Interface, error)
	eval    evaluator
}

type evaluator func(runtime.Object) (WatchResult, bool, error)

func (o *Observer) Observe(
	ctx context.Context,
	ref ResourceRef,
	expectedGeneration int64,
	timeout time.Duration,
) (WatchResult, error) {
	if err := validateRef(ref); err != nil {
		return WatchResult{Resource: ref}, err
	}

	observeCtx, cancel := contextWithOptionalTimeout(ctx, timeout)
	defer cancel()

	source, err := o.resource(ref, expectedGeneration)
	if err != nil {
		return WatchResult{Resource: ref}, err
	}

	fieldSelector := fields.OneTermEqualSelector("metadata.name", ref.Name).String()
	return o.observeLoop(ctx, observeCtx, ref, source, fieldSelector)
}

func (o *Observer) observeLoop(
	parentCtx context.Context,
	observeCtx context.Context,
	ref ResourceRef,
	source resourceSource,
	fieldSelector string,
) (WatchResult, error) {
	last := WatchResult{Resource: ref}
	for {
		object, resourceVersion, listErr := source.current(
			observeCtx,
			metav1.ListOptions{FieldSelector: fieldSelector},
		)
		if listErr != nil {
			return last, classifyError(parentCtx, observeCtx, last, listErr)
		}
		if object == nil {
			return last, &RolloutError{
				Kind: ErrWorkloadUnavailable,
				Last: last,
				Err:  fmt.Errorf("%s/%s was not found", ref.Namespace, ref.Name),
			}
		}

		result, ready, evalErr := source.eval(object)
		last = result
		if evalErr != nil {
			return last, evalErr
		}
		if ready {
			return last, nil
		}

		watcher, watchErr := source.watch(observeCtx, metav1.ListOptions{
			FieldSelector:       fieldSelector,
			ResourceVersion:     resourceVersion,
			AllowWatchBookmarks: true,
		})
		if watchErr != nil {
			if apierrors.IsResourceExpired(watchErr) || apierrors.IsGone(watchErr) {
				continue
			}
			return last, classifyError(parentCtx, observeCtx, last, watchErr)
		}

		watchResult, watchDone, watchErr := consumeWatch(observeCtx, ref, source.eval, last, watcher)
		last = watchResult
		if watchErr == nil {
			return last, nil
		}
		if watchDone {
			return last, classifyError(parentCtx, observeCtx, last, watchErr)
		}
		if apierrors.IsResourceExpired(watchErr) || apierrors.IsGone(watchErr) {
			continue
		}
		if !errors.Is(watchErr, ErrWatchDisconnected) {
			return last, classifyError(parentCtx, observeCtx, last, watchErr)
		}
	}
}

func consumeWatch(
	ctx context.Context,
	ref ResourceRef,
	eval evaluator,
	last WatchResult,
	watcher watch.Interface,
) (WatchResult, bool, error) {
	defer watcher.Stop()
	for {
		select {
		case <-ctx.Done():
			return last, true, ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return last, false, ErrWatchDisconnected
			}
			switch event.Type {
			case watch.Added, watch.Modified:
				result, ready, evalErr := eval(event.Object)
				last = result
				if evalErr != nil {
					return last, true, evalErr
				}
				if ready {
					return last, true, nil
				}
			case watch.Deleted:
				return last, true, fmt.Errorf("%w: %s/%s was deleted", ErrWorkloadUnavailable, ref.Namespace, ref.Name)
			case watch.Error:
				return last, !apierrors.IsResourceExpired(apierrors.FromObject(event.Object)), apierrors.FromObject(event.Object)
			}
		}
	}
}

func contextWithOptionalTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func (o *Observer) resource(ref ResourceRef, expectedGeneration int64) (resourceSource, error) {
	switch ref.GVR {
	case DeploymentGVR:
		client := o.client.AppsV1().Deployments(ref.Namespace)
		return newResourceSource(
			client.List,
			client.Watch,
			func(list *appsv1.DeploymentList) (*appsv1.Deployment, bool) {
				if len(list.Items) == 0 {
					return nil, false
				}
				return &list.Items[0], true
			},
			func(deployment *appsv1.Deployment) (WatchResult, bool, error) {
				return deploymentResult(ref, expectedGeneration, deployment)
			},
		)

	case StatefulSetGVR:
		client := o.client.AppsV1().StatefulSets(ref.Namespace)
		return newResourceSource(
			client.List,
			client.Watch,
			func(list *appsv1.StatefulSetList) (*appsv1.StatefulSet, bool) {
				if len(list.Items) == 0 {
					return nil, false
				}
				return &list.Items[0], true
			},
			func(statefulSet *appsv1.StatefulSet) (WatchResult, bool, error) {
				return statefulSetResult(ref, expectedGeneration, statefulSet)
			},
		)

	case DaemonSetGVR:
		client := o.client.AppsV1().DaemonSets(ref.Namespace)
		return newResourceSource(
			client.List,
			client.Watch,
			func(list *appsv1.DaemonSetList) (*appsv1.DaemonSet, bool) {
				if len(list.Items) == 0 {
					return nil, false
				}
				return &list.Items[0], true
			},
			func(daemonSet *appsv1.DaemonSet) (WatchResult, bool, error) {
				return daemonSetResult(ref, expectedGeneration, daemonSet)
			},
		)

	case JobGVR:
		client := o.client.BatchV1().Jobs(ref.Namespace)
		return newResourceSource(
			client.List,
			client.Watch,
			func(list *batchv1.JobList) (*batchv1.Job, bool) {
				if len(list.Items) == 0 {
					return nil, false
				}
				return &list.Items[0], true
			},
			func(job *batchv1.Job) (WatchResult, bool, error) {
				return jobResult(ref, expectedGeneration, job)
			},
		)

	default:
		return resourceSource{}, fmt.Errorf("%w: %s", ErrUnsupportedResource, ref.GVR.String())
	}
}

func newResourceSource[T runtime.Object, L runtime.Object](
	list func(context.Context, metav1.ListOptions) (L, error),
	watchFunc func(context.Context, metav1.ListOptions) (watch.Interface, error),
	first func(L) (T, bool),
	evaluate func(T) (WatchResult, bool, error),
) (resourceSource, error) {
	return resourceSource{
		current: func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, string, error) {
			objects, err := list(ctx, opts)
			if err != nil {
				return nil, "", err
			}
			object, ok := first(objects)
			if !ok {
				return nil, "", nil
			}
			resourceVersion := ""
			if accessor, ok := any(object).(metav1.Object); ok {
				resourceVersion = accessor.GetResourceVersion()
			}
			if listAccessor, err := meta.ListAccessor(objects); err == nil && listAccessor.GetResourceVersion() != "" {
				resourceVersion = listAccessor.GetResourceVersion()
			}
			return object, resourceVersion, nil
		},
		watch: watchFunc,
		eval: func(obj runtime.Object) (WatchResult, bool, error) {
			typed, ok := obj.(T)
			if !ok {
				return WatchResult{}, false, fmt.Errorf("unexpected workload type %T", obj)
			}
			return evaluate(typed)
		},
	}, nil
}

func validateRef(ref ResourceRef) error {
	if ref.Namespace == "" {
		return fmt.Errorf("resource namespace is required")
	}
	if ref.Name == "" {
		return fmt.Errorf("resource name is required")
	}
	return nil
}

func deploymentResult(ref ResourceRef, expectedGeneration int64, deployment *appsv1.Deployment) (WatchResult, bool, error) {
	conditions := make([]Condition, 0, len(deployment.Status.Conditions))
	available := false
	failed := false
	for _, condition := range deployment.Status.Conditions {
		conditions = append(conditions, Condition{Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason, Message: condition.Message})
		if condition.Type == appsv1.DeploymentAvailable && condition.Status == corev1.ConditionTrue {
			available = true
		}
		if (condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue) ||
			(condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded") {
			failed = true
		}
	}
	result := WatchResult{Resource: ref, Ready: generationReached(deployment.Generation, deployment.Status.ObservedGeneration, expectedGeneration) && available, Failed: failed, ObservedGeneration: deployment.Status.ObservedGeneration, ResourceVersion: deployment.ResourceVersion, Conditions: conditions}
	if failed {
		return result, false, &RolloutError{Kind: ErrWorkloadUnavailable, Last: result}
	}
	return result, result.Ready, nil
}

func statefulSetResult(ref ResourceRef, expectedGeneration int64, statefulSet *appsv1.StatefulSet) (WatchResult, bool, error) {
	conditions := make([]Condition, 0, len(statefulSet.Status.Conditions))
	failed := false
	for _, condition := range statefulSet.Status.Conditions {
		conditions = append(conditions, Condition{Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason, Message: condition.Message})
		if condition.Status == corev1.ConditionFalse {
			failed = true
		}
	}
	desired := int32(1)
	if statefulSet.Spec.Replicas != nil {
		desired = *statefulSet.Spec.Replicas
	}
	result := WatchResult{Resource: ref, Ready: generationReached(statefulSet.Generation, statefulSet.Status.ObservedGeneration, expectedGeneration) && statefulSet.Status.UpdatedReplicas == desired && statefulSet.Status.ReadyReplicas == desired && statefulSet.Status.CurrentRevision == statefulSet.Status.UpdateRevision, Failed: failed, ObservedGeneration: statefulSet.Status.ObservedGeneration, ResourceVersion: statefulSet.ResourceVersion, Conditions: conditions}
	if failed {
		return result, false, &RolloutError{Kind: ErrWorkloadUnavailable, Last: result}
	}
	return result, result.Ready, nil
}

func daemonSetResult(ref ResourceRef, expectedGeneration int64, daemonSet *appsv1.DaemonSet) (WatchResult, bool, error) {
	conditions := make([]Condition, 0, len(daemonSet.Status.Conditions))
	failed := false
	for _, condition := range daemonSet.Status.Conditions {
		conditions = append(conditions, Condition{Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason, Message: condition.Message})
		if condition.Status == corev1.ConditionFalse {
			failed = true
		}
	}
	result := WatchResult{Resource: ref, Ready: generationReached(daemonSet.Generation, daemonSet.Status.ObservedGeneration, expectedGeneration) && daemonSet.Status.UpdatedNumberScheduled == daemonSet.Status.DesiredNumberScheduled && daemonSet.Status.NumberAvailable == daemonSet.Status.DesiredNumberScheduled && daemonSet.Status.NumberUnavailable == 0, Failed: failed, ObservedGeneration: daemonSet.Status.ObservedGeneration, ResourceVersion: daemonSet.ResourceVersion, Conditions: conditions}
	if failed {
		return result, false, &RolloutError{Kind: ErrWorkloadUnavailable, Last: result}
	}
	return result, result.Ready, nil
}

func jobResult(ref ResourceRef, expectedGeneration int64, job *batchv1.Job) (WatchResult, bool, error) {
	conditions := make([]Condition, 0, len(job.Status.Conditions))
	complete := false
	failed := false
	for _, condition := range job.Status.Conditions {
		conditions = append(conditions, Condition{Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason, Message: condition.Message})
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			complete = true
		}
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			failed = true
		}
	}
	result := WatchResult{Resource: ref, Ready: generationReached(job.Generation, job.Generation, expectedGeneration) && complete, Failed: failed, ObservedGeneration: job.Generation, ResourceVersion: job.ResourceVersion, Conditions: conditions}
	if failed {
		return result, false, &RolloutError{Kind: ErrWorkloadUnavailable, Last: result}
	}
	return result, result.Ready, nil
}

func generationReached(metadataGeneration, observedGeneration, expectedGeneration int64) bool {
	target := expectedGeneration
	if target == 0 {
		target = metadataGeneration
	}
	return observedGeneration >= target
}

func classifyError(parentCtx, observeCtx context.Context, last WatchResult, err error) error {
	if parentCtx.Err() != nil {
		return &RolloutError{Kind: ErrCancelled, Last: last, Err: parentCtx.Err()}
	}
	if errors.Is(observeCtx.Err(), context.DeadlineExceeded) {
		return &RolloutError{Kind: ErrRolloutTimeout, Last: last, Err: observeCtx.Err()}
	}
	if apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
		return &RolloutError{Kind: ErrResourceVersionExpired, Last: last, Err: err}
	}
	if errors.Is(err, ErrWorkloadUnavailable) {
		var rolloutErr *RolloutError
		if errors.As(err, &rolloutErr) {
			return rolloutErr
		}
		return &RolloutError{Kind: ErrWorkloadUnavailable, Last: last, Err: err}
	}
	if errors.Is(err, ErrWatchDisconnected) {
		return &RolloutError{Kind: ErrWatchDisconnected, Last: last, Err: err}
	}
	return &RolloutError{Kind: ErrWatchDisconnected, Last: last, Err: err}
}

var _ RolloutObserver = (*Observer)(nil)
