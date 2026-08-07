package preflight

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

func (p *PullProber) Probe(ctx context.Context, input PullInput) (*PullBatchResult, error) {
	if p == nil || p.client == nil {
		return nil, fmt.Errorf("%w: kubernetes client is required", ErrPullInputInvalid)
	}
	if err := validatePullInput(input); err != nil {
		return nil, err
	}
	startedAt := p.now()
	result := &PullBatchResult{
		OperationID:    input.OperationID,
		Namespace:      input.Namespace,
		ServiceAccount: input.ServiceAccount,
		Results:        make([]ImagePullResult, 0, len(input.Images)),
	}

	timeout := input.Timeout
	if timeout <= 0 {
		timeout = DefaultPullTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for _, image := range input.Images {
		imageResult := p.probeImage(probeCtx, input, image)
		result.Results = append(result.Results, imageResult)
		if imageResult.CleanupFailed {
			result.CleanupFailed = true
			result.Warning = ErrCleanupFailed
		}
	}
	result.Passed = true
	for _, imageResult := range result.Results {
		if !imageResult.Pulled || imageResult.CleanupFailed {
			result.Passed = false
			break
		}
	}
	result.Duration = p.now().Sub(startedAt)
	return result, nil
}

func (p *PullProber) probeImage(ctx context.Context, input PullInput, image string) ImagePullResult {
	startedAt := p.now()
	result := ImagePullResult{Image: image, State: PullStateCreated}
	pod := buildProbePod(input, image, startedAt)
	pod.Annotations[ExpireAtAnnotation] = startedAt.Add(p.probeTTL).UTC().Format(time.RFC3339)

	created, err := p.client.CoreV1().Pods(input.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		result.State = PullStateFailed
		result.ErrorCode = classifyCreateError(err)
		result.Reason = sanitizePullReason(err.Error())
		result.Duration = p.now().Sub(startedAt)
		return result
	}
	result.ProbeName = created.Name
	result.State = PullStatePulling

	observed := p.waitForPull(ctx, input.Namespace, created.Name, created.ResourceVersion)
	observed.Image = image
	observed.ProbeName = created.Name
	observed.Duration = p.now().Sub(startedAt)
	if observed.Node == "" {
		observed.Node = created.Spec.NodeName
	}

	if shouldCleanup(input.CleanupPolicy, observed.Pulled) {
		cleanupErr := p.cleanupPod(ctx, input.Namespace, created.Name)
		if cleanupErr != nil {
			observed.CleanupFailed = true
			if observed.Reason == "" {
				observed.Reason = sanitizePullReason(cleanupErr.Error())
			}
			p.logger.Warn(
				"runtime pull preflight cleanup failed",
				"operation_id", input.OperationID,
				"namespace", input.Namespace,
				"pod", created.Name,
				"error", cleanupErr,
			)
		} else if observed.Pulled {
			observed.State = PullStateCleaned
		}
	}

	return observed
}

func (p *PullProber) waitForPull(
	ctx context.Context,
	namespace string,
	name string,
	resourceVersion string,
) ImagePullResult {
	pods := p.client.CoreV1().Pods(namespace)
	current, err := pods.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if result, done := pullResultFromPod(current); done {
			return result
		}
		resourceVersion = current.ResourceVersion
	}

	watcher, err := pods.Watch(ctx, metav1.ListOptions{
		FieldSelector:   "metadata.name=" + name,
		ResourceVersion: resourceVersion,
	})
	if err != nil {
		return ImagePullResult{State: PullStateFailed, ErrorCode: ErrPullUnknown, Reason: sanitizePullReason(err.Error())}
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return ImagePullResult{
				State:     PullStateTimeout,
				ErrorCode: ErrPullTimeout,
				Reason:    sanitizePullReason(ctx.Err().Error()),
			}
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return ImagePullResult{State: PullStateFailed, ErrorCode: ErrPullUnknown, Reason: "pod watch closed"}
			}
			if event.Type == watch.Error {
				return ImagePullResult{State: PullStateFailed, ErrorCode: ErrPullUnknown, Reason: "pod watch error"}
			}
			typedPod, ok := event.Object.(*corev1.Pod)
			if !ok || typedPod.Name != name {
				continue
			}
			if result, done := pullResultFromPod(typedPod); done {
				return result
			}
		}
	}
}

func pullResultFromPod(pod *corev1.Pod) (ImagePullResult, bool) {
	result := ImagePullResult{State: PullStatePulling, Node: pod.Spec.NodeName}
	if imagePulled(pod) {
		result.Pulled = true
		result.State = PullStateSucceeded
		return result, true
	}
	if code, reason, failed := classifyPullFailure(pod); failed {
		result.State = PullStateFailed
		result.ErrorCode = code
		result.Reason = reason
		return result, true
	}
	if pod.Status.Phase == corev1.PodFailed {
		result.State = PullStateFailed
		result.ErrorCode = ErrPullUnknown
		result.Reason = sanitizePullReason(pod.Status.Message)
		return result, true
	}
	return result, false
}

func classifyCreateError(err error) string {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return ErrRegistryUnauthorized
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return ErrPullTimeout
	default:
		return ErrPullUnknown
	}
}

func shouldCleanup(policy CleanupPolicy, pulled bool) bool {
	switch policy {
	case CleanupBackground:
		return false
	case CleanupOnSuccess:
		return pulled
	default:
		return true
	}
}
