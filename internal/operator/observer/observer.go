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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

type RolloutObserver interface {
	Observe(ctx context.Context, ref ResourceRef, expectedGeneration int64, timeout time.Duration) (WatchResult, error)
	// ObserveWithProgress is Observe with an onProgress callback invoked
	// after every evaluation round (including the initial list result) so
	// the caller can report ready/desired counters (REQ-077 AC-077-02).
	ObserveWithProgress(ctx context.Context, ref ResourceRef, expectedGeneration int64, timeout time.Duration, onProgress func(WatchResult)) (WatchResult, error)
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
	return o.ObserveWithProgress(ctx, ref, expectedGeneration, timeout, nil)
}

func (o *Observer) ObserveWithProgress(
	ctx context.Context,
	ref ResourceRef,
	expectedGeneration int64,
	timeout time.Duration,
	onProgress func(WatchResult),
) (WatchResult, error) {
	if err := validateObserveInput(ctx, ref, expectedGeneration, timeout); err != nil {
		return WatchResult{Resource: ref}, err
	}

	observeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	source, err := o.resource(ref, expectedGeneration)
	if err != nil {
		return WatchResult{Resource: ref}, err
	}

	fieldSelector := fields.OneTermEqualSelector("metadata.name", ref.Name).String()
	return o.observeLoop(ctx, observeCtx, ref, source, fieldSelector, onProgress)
}

func (o *Observer) observeLoop(
	parentCtx context.Context,
	observeCtx context.Context,
	ref ResourceRef,
	source resourceSource,
	fieldSelector string,
	onProgress func(WatchResult),
) (WatchResult, error) {
	last := WatchResult{Resource: ref}
	var lockedUID types.UID
	for {
		var resourceVersion string
		var ready bool
		var err error
		last, lockedUID, resourceVersion, ready, err = listAndEvaluate(
			observeCtx,
			source,
			fieldSelector,
			last,
			lockedUID,
		)
		if err != nil {
			return last, classifyError(parentCtx, observeCtx, last, err)
		}
		if onProgress != nil {
			onProgress(last)
		}
		if ready {
			if parentCtx.Err() != nil {
				return last, classifyError(parentCtx, observeCtx, last, parentCtx.Err())
			}
			return last, nil
		}

		watcher, watchErr := source.watch(observeCtx, metav1.ListOptions{
			FieldSelector:       fieldSelector,
			ResourceVersion:     resourceVersion,
			AllowWatchBookmarks: true,
		})
		if watchErr != nil {
			if isResourceVersionExpired(watchErr) {
				continue
			}
			return last, classifyError(parentCtx, observeCtx, last, watchErr)
		}

		last, watchErr = consumeWatch(observeCtx, source.eval, last, watcher, lockedUID, onProgress)
		if watchErr == nil {
			if parentCtx.Err() != nil {
				return last, classifyError(parentCtx, observeCtx, last, parentCtx.Err())
			}
			return last, nil
		}
		if isRecoverableWatchError(watchErr) {
			continue
		}
		return last, classifyError(parentCtx, observeCtx, last, watchErr)
	}
}

func listAndEvaluate(
	ctx context.Context,
	source resourceSource,
	fieldSelector string,
	last WatchResult,
	lockedUID types.UID,
) (result WatchResult, uid types.UID, resourceVersion string, ready bool, err error) {
	object, resourceVersion, err := source.current(ctx, metav1.ListOptions{FieldSelector: fieldSelector})
	if err != nil {
		return last, lockedUID, "", false, err
	}
	if object == nil {
		return last, lockedUID, resourceVersion, false, unavailableError(last, "object not found")
	}

	result, ready, err = source.eval(object)
	if lockedUID == "" {
		lockedUID = result.ResourceUID
	}
	if result.ResourceUID != lockedUID {
		return last, lockedUID, resourceVersion, false, unavailableError(last, "object UID changed")
	}
	if err != nil {
		return result, lockedUID, resourceVersion, false, err
	}
	return result, lockedUID, resourceVersion, ready, nil
}

func consumeWatch(
	ctx context.Context,
	eval evaluator,
	last WatchResult,
	watcher watch.Interface,
	lockedUID types.UID,
	onProgress func(WatchResult),
) (WatchResult, error) {
	defer watcher.Stop()
	for {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return last, ErrWatchDisconnected
			}
			switch event.Type {
			case watch.Added, watch.Modified:
				result, ready, err := eval(event.Object)
				if result.ResourceUID != lockedUID {
					return last, unavailableError(last, "object UID changed")
				}
				last = result
				if onProgress != nil {
					onProgress(last)
				}
				if err != nil {
					return last, err
				}
				if ready {
					return last, nil
				}
			case watch.Deleted:
				return last, unavailableError(last, "object deleted")
			case watch.Bookmark:
			case watch.Error:
				return last, apierrors.FromObject(event.Object)
			}
		}
	}
}

func isRecoverableWatchError(err error) bool {
	return errors.Is(err, ErrWatchDisconnected) || isResourceVersionExpired(err)
}

func isResourceVersionExpired(err error) bool {
	return apierrors.IsResourceExpired(err) || apierrors.IsGone(err)
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

func validateObserveInput(ctx context.Context, ref ResourceRef, expectedGeneration int64, timeout time.Duration) error {
	if ctx == nil {
		return invalidArgument(ref, "context", "must not be nil")
	}
	if ctx.Err() != nil {
		return &RolloutError{
			Kind:  ErrCancelled,
			Last:  WatchResult{Resource: ref},
			Err:   fmt.Errorf("rollout watch cancelled: %s/%s", ref.Namespace, ref.Name),
			cause: ctx.Err(),
		}
	}
	if ref.Namespace == "" {
		return invalidArgument(ref, "namespace", "must not be empty")
	}
	if ref.Name == "" {
		return invalidArgument(ref, "name", "must not be empty")
	}
	switch ref.GVR {
	case DeploymentGVR, StatefulSetGVR, DaemonSetGVR:
		if expectedGeneration <= 0 {
			return invalidArgument(ref, "expectedGeneration", "must be greater than zero for apps workloads")
		}
	case JobGVR:
		if expectedGeneration != 0 {
			return invalidArgument(ref, "expectedGeneration", "must be zero for jobs")
		}
	default:
		return &RolloutError{
			Kind: ErrUnsupportedResource,
			Last: WatchResult{Resource: ref},
			Err:  fmt.Errorf("unsupported rollout resource: %s", ref.GVR.String()),
		}
	}
	if timeout <= 0 {
		return invalidArgument(ref, "timeout", "must be greater than zero")
	}
	return nil
}

func invalidArgument(ref ResourceRef, field, reason string) error {
	return &RolloutError{
		Kind: ErrInvalidArgument,
		Last: WatchResult{Resource: ref},
		Err:  fmt.Errorf("invalid rollout watch argument: %s: %s", field, reason),
	}
}

func deploymentResult(
	ref ResourceRef,
	expectedGeneration int64,
	deployment *appsv1.Deployment,
) (WatchResult, bool, error) {
	conditions := make([]Condition, 0, len(deployment.Status.Conditions))
	available := false
	failed := false
	for _, condition := range deployment.Status.Conditions {
		conditions = append(conditions, rolloutCondition(
			string(condition.Type),
			string(condition.Status),
			condition.Reason,
			condition.Message,
		))
		switch condition.Type {
		case appsv1.DeploymentAvailable:
			available = condition.Status == corev1.ConditionTrue
		case appsv1.DeploymentReplicaFailure:
			failed = failed || condition.Status == corev1.ConditionTrue
		case appsv1.DeploymentProgressing:
			failed = failed || (condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded")
		}
	}
	genReached := generationReached(deployment.Generation, deployment.Status.ObservedGeneration, expectedGeneration)
	replicas := int32(1)
	if deployment.Spec.Replicas != nil {
		replicas = *deployment.Spec.Replicas
	}
	countersOK := deployment.Status.UpdatedReplicas == replicas &&
		deployment.Status.AvailableReplicas == replicas &&
		deployment.Status.UnavailableReplicas == 0
	result := WatchResult{
		Resource:           ref,
		ResourceUID:        deployment.UID,
		Generation:         deployment.Generation,
		ObservedGeneration: deployment.Status.ObservedGeneration,
		ResourceVersion:    deployment.ResourceVersion,
		Ready:              genReached && available && countersOK && !failed,
		Failed:             failed,
		Conditions:         conditions,
		ReadyCount:         deployment.Status.AvailableReplicas,
		DesiredCount:       replicas,
	}
	if failed {
		return result, false, unavailableError(result, "deployment reported terminal failure")
	}
	return result, result.Ready, nil
}

func statefulSetResult(
	ref ResourceRef,
	expectedGeneration int64,
	statefulSet *appsv1.StatefulSet,
) (WatchResult, bool, error) {
	conditions := make([]Condition, 0, len(statefulSet.Status.Conditions))
	for _, condition := range statefulSet.Status.Conditions {
		conditions = append(conditions, rolloutCondition(
			string(condition.Type),
			string(condition.Status),
			condition.Reason,
			condition.Message,
		))
	}
	desired := int32(1)
	if statefulSet.Spec.Replicas != nil {
		desired = *statefulSet.Spec.Replicas
	}
	generationReady := generationReached(
		statefulSet.Generation,
		statefulSet.Status.ObservedGeneration,
		expectedGeneration,
	)
	replicasReady := statefulSet.Status.UpdatedReplicas == desired &&
		statefulSet.Status.ReadyReplicas == desired
	revisionsMatch := statefulSet.Status.CurrentRevision == statefulSet.Status.UpdateRevision
	result := WatchResult{
		Resource:           ref,
		ResourceUID:        statefulSet.UID,
		Generation:         statefulSet.Generation,
		ObservedGeneration: statefulSet.Status.ObservedGeneration,
		ResourceVersion:    statefulSet.ResourceVersion,
		Ready:              generationReady && replicasReady && revisionsMatch,
		Failed:             false,
		Conditions:         conditions,
		ReadyCount:         statefulSet.Status.ReadyReplicas,
		DesiredCount:       desired,
	}
	return result, result.Ready, nil
}

func daemonSetResult(
	ref ResourceRef,
	expectedGeneration int64,
	daemonSet *appsv1.DaemonSet,
) (WatchResult, bool, error) {
	conditions := make([]Condition, 0, len(daemonSet.Status.Conditions))
	for _, condition := range daemonSet.Status.Conditions {
		conditions = append(conditions, rolloutCondition(
			string(condition.Type),
			string(condition.Status),
			condition.Reason,
			condition.Message,
		))
	}
	generationReady := generationReached(
		daemonSet.Generation,
		daemonSet.Status.ObservedGeneration,
		expectedGeneration,
	)
	scheduledReady := daemonSet.Status.UpdatedNumberScheduled == daemonSet.Status.DesiredNumberScheduled
	availabilityReady := daemonSet.Status.NumberAvailable == daemonSet.Status.DesiredNumberScheduled &&
		daemonSet.Status.NumberUnavailable == 0
	result := WatchResult{
		Resource:           ref,
		ResourceUID:        daemonSet.UID,
		Generation:         daemonSet.Generation,
		ObservedGeneration: daemonSet.Status.ObservedGeneration,
		ResourceVersion:    daemonSet.ResourceVersion,
		Ready:              generationReady && scheduledReady && availabilityReady,
		Failed:             false,
		Conditions:         conditions,
		ReadyCount:         daemonSet.Status.NumberAvailable,
		DesiredCount:       daemonSet.Status.DesiredNumberScheduled,
	}
	return result, result.Ready, nil
}
func jobResult(ref ResourceRef, _ int64, job *batchv1.Job) (WatchResult, bool, error) {
	conditions := make([]Condition, 0, len(job.Status.Conditions))
	complete := false
	failed := false
	for _, condition := range job.Status.Conditions {
		conditions = append(conditions, rolloutCondition(
			string(condition.Type),
			string(condition.Status),
			condition.Reason,
			condition.Message,
		))
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			complete = true
		}
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			failed = true
		}
	}
	result := WatchResult{
		Resource:           ref,
		ResourceUID:        job.UID,
		Generation:         job.Generation,
		ObservedGeneration: 0,
		ResourceVersion:    job.ResourceVersion,
		Ready:              complete && !failed,
		Failed:             failed,
		Conditions:         conditions,
	}
	if failed {
		return result, false, unavailableError(result, "job reported terminal failure")
	}
	return result, result.Ready, nil
}

func rolloutCondition(conditionType, status, reason, message string) Condition {
	return Condition{
		Type:    conditionType,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
}

func generationReached(metadataGeneration, observedGeneration, expectedGeneration int64) bool {
	return metadataGeneration >= expectedGeneration && observedGeneration >= expectedGeneration
}

func classifyError(parentCtx, observeCtx context.Context, last WatchResult, err error) error {
	if parentCtx.Err() != nil {
		return &RolloutError{
			Kind:  ErrCancelled,
			Last:  last,
			Err:   fmt.Errorf("rollout watch cancelled: %s/%s", last.Resource.Namespace, last.Resource.Name),
			cause: parentCtx.Err(),
		}
	}
	if errors.Is(observeCtx.Err(), context.DeadlineExceeded) {
		return &RolloutError{
			Kind:  ErrRolloutTimeout,
			Last:  last,
			Err:   fmt.Errorf("rollout watch timed out: %s/%s", last.Resource.Namespace, last.Resource.Name),
			cause: observeCtx.Err(),
		}
	}
	if errors.Is(err, ErrWorkloadUnavailable) {
		var rolloutErr *RolloutError
		if errors.As(err, &rolloutErr) {
			return rolloutErr
		}
		return unavailableError(last, err.Error())
	}
	return &RolloutError{
		Kind:  ErrWatchDisconnected,
		Last:  last,
		Err:   fmt.Errorf("rollout watch failed: %s/%s: %v", last.Resource.Namespace, last.Resource.Name, err),
		cause: err,
	}
}

func unavailableError(last WatchResult, reason string) error {
	return &RolloutError{
		Kind: ErrWorkloadUnavailable,
		Last: last,
		Err: fmt.Errorf(
			"rollout workload unavailable: %s/%s: %s",
			last.Resource.Namespace,
			last.Resource.Name,
			reason,
		),
	}
}

var _ RolloutObserver = (*Observer)(nil)
