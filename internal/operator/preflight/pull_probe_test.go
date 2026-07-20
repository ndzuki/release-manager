package preflight

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	testImageOne = "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testImageTwo = "registry.example/sidecar@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestBuildProbePodUsesTargetRuntimeIdentity(t *testing.T) {
	t.Parallel()

	pod := buildProbePod(PullInput{
		OperationID:    "operation-1",
		Namespace:      "target",
		ServiceAccount: "runtime-sa",
		Images:         []string{testImageOne},
	}, testImageOne, time.Unix(100, 0))

	assert.Equal(t, "target", pod.Namespace)
	assert.Equal(t, "runtime-sa", pod.Spec.ServiceAccountName)
	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)
	assert.False(t, *pod.Spec.AutomountServiceAccountToken)
	require.Len(t, pod.Spec.Containers, 1)
	container := pod.Spec.Containers[0]
	assert.Equal(t, testImageOne, container.Image)
	assert.Equal(t, corev1.PullAlways, container.ImagePullPolicy)
	assert.Equal(t, []string{"/bin/true"}, container.Command)
	assert.False(t, *container.SecurityContext.AllowPrivilegeEscalation)
	assert.True(t, *container.SecurityContext.ReadOnlyRootFilesystem)
	assert.Equal(t, "true", pod.Labels[ManagedLabel])
	assert.NotEmpty(t, pod.Labels[OperationLabel])
	assert.NotEmpty(t, pod.Annotations[ExpireAtAnnotation])
}

func TestClassifyPullFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
		want    string
	}{
		{name: "registry unauthorized", message: "failed to authorize: authentication required", want: ErrRegistryUnauthorized},
		{name: "iam denied", message: "AccessDenied: not authorized to perform ecr:GetAuthorizationToken", want: ErrIAMDenied},
		{name: "network", message: "dial tcp: network is unreachable", want: ErrNetworkUnreachable},
		{name: "backoff", message: "ImagePullBackOff", want: ErrImagePullBackOff},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pod := waitingPod("probe", "node-a", tt.message)
			code, _, failed := classifyPullFailure(pod)
			assert.True(t, failed)
			assert.Equal(t, tt.want, code)
		})
	}
}

func TestPullProberMultipleImagesTracksIndividualResults(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	installCreateStatusReactor(client, map[string]*corev1.PodStatus{
		testImageOne: runningStatus("node-a"),
		testImageTwo: waitingStatus("node-b", "AccessDenied: IAM role cannot pull image"),
	})
	prober := NewPullProber(client, discardLogger())

	result, err := prober.Probe(t.Context(), PullInput{
		OperationID:    "operation-1",
		Namespace:      "target",
		ServiceAccount: "runtime-sa",
		Images:         []string{testImageOne, testImageTwo},
		CleanupPolicy:  CleanupAlways,
	})
	require.NoError(t, err)
	require.Len(t, result.Results, 2)
	assert.False(t, result.Passed)
	assert.Equal(t, testImageOne, result.Results[0].Image)
	assert.True(t, result.Results[0].Pulled)
	assert.Equal(t, "node-a", result.Results[0].Node)
	assert.Equal(t, testImageTwo, result.Results[1].Image)
	assert.False(t, result.Results[1].Pulled)
	assert.Equal(t, ErrIAMDenied, result.Results[1].ErrorCode)
	assert.Equal(t, "node-b", result.Results[1].Node)

	assert.Equal(t, 2, countActions(client.Actions(), "create", "pods"))
	assert.Equal(t, 2, countActions(client.Actions(), "delete", "pods"))
}

func TestPullProberTimeoutStillCleansPod(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	prober := NewPullProber(client, discardLogger())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	result, err := prober.Probe(ctx, PullInput{
		OperationID:   "operation-timeout",
		Namespace:     "target",
		Images:        []string{testImageOne},
		CleanupPolicy: CleanupAlways,
	})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	assert.Equal(t, ErrPullTimeout, result.Results[0].ErrorCode)
	assert.Equal(t, PullStateTimeout, result.Results[0].State)
	assert.Equal(t, 1, countActions(client.Actions(), "delete", "pods"))
}

func TestPullProberCleanupFailureIsWarning(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	installCreateStatusReactor(client, map[string]*corev1.PodStatus{
		testImageOne: runningStatus("node-a"),
	})
	client.PrependReactor("delete", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	prober := NewPullProber(client, discardLogger())

	result, err := prober.Probe(t.Context(), PullInput{
		OperationID:   "operation-cleanup",
		Namespace:     "target",
		Images:        []string{testImageOne},
		CleanupPolicy: CleanupAlways,
	})
	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.True(t, result.CleanupFailed)
	assert.Equal(t, ErrCleanupFailed, result.Warning)
	assert.True(t, result.Results[0].Pulled)
	assert.True(t, result.Results[0].CleanupFailed)
}

func TestPullGCDeletesOnlyExpiredManagedPods(t *testing.T) {
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	expired := managedPod("expired", now.Add(-time.Minute))
	future := managedPod("future", now.Add(time.Minute))
	unmanaged := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "business", Namespace: "target"}}
	client := kubernetesfake.NewSimpleClientset(expired, future, unmanaged)
	gc := NewPullGC(client, "target", discardLogger())
	gc.now = func() time.Time { return now }

	deleted, err := gc.RunOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	_, err = client.CoreV1().Pods("target").Get(t.Context(), "expired", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
	_, err = client.CoreV1().Pods("target").Get(t.Context(), "future", metav1.GetOptions{})
	require.NoError(t, err)
	_, err = client.CoreV1().Pods("target").Get(t.Context(), "business", metav1.GetOptions{})
	require.NoError(t, err)
}

func TestPullInputRejectsUnpinnedImageAndArbitraryCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input PullInput
		want  error
	}{
		{
			name:  "tagged image",
			input: PullInput{OperationID: "op", Namespace: "ns", Images: []string{"registry.example/app:latest"}},
			want:  ErrUnpinnedImage,
		},
		{
			name:  "arbitrary command",
			input: PullInput{OperationID: "op", Namespace: "ns", Images: []string{testImageOne}, ProbeCommand: []string{"/bin/sh"}},
			want:  ErrUntrustedProbeCommand,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, validatePullInput(tt.input), tt.want)
		})
	}
}

func installCreateStatusReactor(client *kubernetesfake.Clientset, statuses map[string]*corev1.PodStatus) {
	client.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(ktesting.CreateAction)
		if !ok {
			return true, nil, errors.New("unexpected create action")
		}
		createdPod, ok := create.GetObject().(*corev1.Pod)
		if !ok {
			return true, nil, errors.New("unexpected create object")
		}
		pod := createdPod.DeepCopy()
		pod.Name = pod.GenerateName + "test"
		pod.ResourceVersion = "1"
		status := statuses[pod.Spec.Containers[0].Image]
		if status != nil {
			pod.Status = *status.DeepCopy()
			pod.Spec.NodeName = status.HostIP
		}
		require.NoError(&testing.T{}, client.Tracker().Add(pod))
		return true, pod, nil
	})
}

func runningStatus(node string) *corev1.PodStatus {
	return &corev1.PodStatus{
		HostIP: node,
		Phase:  corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "pull-probe",
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}},
	}
}

func waitingStatus(node, message string) *corev1.PodStatus {
	return &corev1.PodStatus{
		HostIP: node,
		Phase:  corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name: "pull-probe",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
				Reason:  "ErrImagePull",
				Message: message,
			}},
		}},
	}
}

func waitingPod(name, node, message string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     *waitingStatus(node, message),
	}
}

func managedPod(name string, expiresAt time.Time) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      name,
		Namespace: "target",
		Labels:    map[string]string{ManagedLabel: "true"},
		Annotations: map[string]string{
			ExpireAtAnnotation: expiresAt.Format(time.RFC3339),
		},
	}}
}

func countActions(actions []ktesting.Action, verb, resource string) int {
	count := 0
	for _, action := range actions {
		if action.GetVerb() == verb && action.GetResource() == (schema.GroupVersionResource{Version: "v1", Resource: resource}) {
			count++
		}
	}
	return count
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
