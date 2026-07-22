//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"

	"github.com/ndzuki/release-manager/internal/operator/observer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rolloutImage = "registry.k8s.io/pause:3.10"
	jobImage     = "busybox:1.36"
	watchTimeout = 90 * time.Second
)

func TestRolloutWatchReadyWorkloads(t *testing.T) {
	client, _ := integrationClient(t)
	namespace := createNamespace(t, client)
	obs := observer.New(client)

	deployment := createDeployment(t, client, namespace, "deployment")
	statefulSet := createStatefulSet(t, client, namespace, "statefulset")
	daemonSet := createDaemonSet(t, client, namespace, "daemonset")
	job := createJob(t, client, namespace, "job")

	readyTests := []struct {
		name string
		ref  observer.ResourceRef
		gen  int64
	}{
		{
			name: "deployment",
			ref:  observer.ResourceRef{GVR: observer.DeploymentGVR, Namespace: namespace, Name: deployment.Name},
			gen:  deployment.Generation,
		},
		{
			name: "statefulset",
			ref:  observer.ResourceRef{GVR: observer.StatefulSetGVR, Namespace: namespace, Name: statefulSet.Name},
			gen:  statefulSet.Generation,
		},
		{
			name: "daemonset",
			ref:  observer.ResourceRef{GVR: observer.DaemonSetGVR, Namespace: namespace, Name: daemonSet.Name},
			gen:  daemonSet.Generation,
		},
		{
			name: "job",
			ref:  observer.ResourceRef{GVR: observer.JobGVR, Namespace: namespace, Name: job.Name},
			gen:  job.Generation,
		},
	}
	for _, test := range readyTests {
		t.Run(test.name, func(t *testing.T) {
			result, err := obs.Observe(t.Context(), test.ref, test.gen, watchTimeout)
			require.NoError(t, err)
			assert.True(t, result.Ready)
			assert.GreaterOrEqual(t, result.ObservedGeneration, test.gen)
			assert.NotEmpty(t, result.ResourceVersion)
		})
	}
}

func TestRolloutWatchReconnectsAfterTransportDisconnect(t *testing.T) {
	baseClient, baseConfig := integrationClient(t)
	namespace := createNamespace(t, baseClient)
	deployment := createDeployment(t, baseClient, namespace, "reconnect")
	ref := observer.ResourceRef{GVR: observer.DeploymentGVR, Namespace: namespace, Name: deployment.Name}

	transport := &disconnectingTransport{disconnectFirstWatch: true}
	client := clientWithTransport(t, baseConfig, transport)
	resultCh := make(chan observer.WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := observer.New(client).Observe(t.Context(), ref, deployment.Generation+1, watchTimeout)
		resultCh <- result
		errCh <- err
	}()

	require.Eventually(t, func() bool { return transport.watchCalls.Load() >= 1 }, 5*time.Second, 10*time.Millisecond)
	updated, err := updateDeploymentTemplateAnnotation(t, baseClient, namespace, deployment.Name, "ready")
	require.NoError(t, err)

	select {
	case err := <-errCh:
		result := <-resultCh
		require.NoError(t, err)
		assert.True(t, result.Ready)
		assert.GreaterOrEqual(t, result.ObservedGeneration, updated.Generation)
		assert.NotEmpty(t, result.ResourceVersion)
		assert.GreaterOrEqual(t, transport.listCalls.Load(), int64(2))
		assert.GreaterOrEqual(t, transport.watchCalls.Load(), int64(2))
		assertResourceVersionsDoNotRegress(t, transport.resourceVersions())
	case <-time.After(watchTimeout):
		t.Fatal("observer did not recover after transport disconnect")
	}
}

func TestRolloutWatchReListsAfterResourceVersionExpired(t *testing.T) {
	baseClient, baseConfig := integrationClient(t)
	namespace := createNamespace(t, baseClient)
	deployment := createDeployment(t, baseClient, namespace, "expired")
	ref := observer.ResourceRef{GVR: observer.DeploymentGVR, Namespace: namespace, Name: deployment.Name}

	transport := &expiredTransport{}
	client := clientWithTransport(t, baseConfig, transport)
	resultCh := make(chan observer.WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := observer.New(client).Observe(t.Context(), ref, deployment.Generation+1, watchTimeout)
		resultCh <- result
		errCh <- err
	}()

	require.Eventually(t, func() bool { return transport.watchCalls.Load() >= 2 }, 5*time.Second, 10*time.Millisecond)
	updated, err := updateDeploymentTemplateAnnotation(t, baseClient, namespace, deployment.Name, "expired-recovery")
	require.NoError(t, err)

	select {
	case err := <-errCh:
		result := <-resultCh
		require.NoError(t, err)
		assert.True(t, result.Ready)
		assert.NotEmpty(t, result.ResourceVersion)
		assert.GreaterOrEqual(t, transport.listCalls.Load(), int64(2))
		assert.GreaterOrEqual(t, result.ObservedGeneration, updated.Generation)
		assertResourceVersionsDoNotRegress(t, transport.resourceVersions())
	case <-time.After(watchTimeout):
		t.Fatal("observer did not recover after resource version expiration")
	}
}

func TestRolloutWatchTimeoutAndCancellationCleanup(t *testing.T) {
	client, config := integrationClient(t)
	namespace := createNamespace(t, client)
	deployment := createDeployment(t, client, namespace, "timeout")
	ref := observer.ResourceRef{GVR: observer.DeploymentGVR, Namespace: namespace, Name: deployment.Name}

	t.Run("timeout", func(t *testing.T) {
		transport := &trackingTransport{}
		watchClient := clientWithTransport(t, config, transport)
		result, err := observer.New(watchClient).Observe(t.Context(), ref, deployment.Generation+1, 300*time.Millisecond)

		assert.ErrorIs(t, err, observer.ErrRolloutTimeout)
		assert.False(t, result.Ready)
		assert.Equal(t, deployment.Generation, result.ObservedGeneration)
		var rolloutErr *observer.RolloutError
		require.ErrorAs(t, err, &rolloutErr)
		assert.Equal(t, result, rolloutErr.Last)
		assert.Eventually(t, func() bool { return transport.activeWatches.Load() == 0 }, time.Second, 10*time.Millisecond)
	})

	t.Run("cancel", func(t *testing.T) {
		transport := &trackingTransport{}
		watchClient := clientWithTransport(t, config, transport)
		ctx, cancel := context.WithCancel(t.Context())
		resultCh := make(chan observer.WatchResult, 1)
		errCh := make(chan error, 1)
		go func() {
			result, err := observer.New(watchClient).Observe(ctx, ref, deployment.Generation+1, watchTimeout)
			resultCh <- result
			errCh <- err
		}()

		require.Eventually(t, func() bool { return transport.activeWatches.Load() == 1 }, time.Second, 10*time.Millisecond)
		cancel()

		select {
		case err := <-errCh:
			result := <-resultCh
			assert.ErrorIs(t, err, observer.ErrCancelled)
			assert.False(t, result.Ready)
			var rolloutErr *observer.RolloutError
			require.ErrorAs(t, err, &rolloutErr)
			assert.Equal(t, result, rolloutErr.Last)
		case <-time.After(time.Second):
			t.Fatal("observer did not stop after context cancellation")
		}
		assert.Eventually(t, func() bool { return transport.activeWatches.Load() == 0 }, time.Second, 10*time.Millisecond)
	})
}

type disconnectingTransport struct {
	base                 http.RoundTripper
	disconnectFirstWatch bool
	listCalls            atomic.Int64
	watchCalls           atomic.Int64
	mu                   sync.Mutex
	watchVersions        []string
}

type expiredTransport struct {
	base          http.RoundTripper
	listCalls     atomic.Int64
	watchCalls    atomic.Int64
	mu            sync.Mutex
	watchVersions []string
}

func (t *disconnectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Query().Get("watch") != "true" {
		t.listCalls.Add(1)
	}
	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.URL.Query().Get("watch") != "true" {
		return response, nil
	}
	t.mu.Lock()
	t.watchVersions = append(t.watchVersions, req.URL.Query().Get("resourceVersion"))
	t.mu.Unlock()
	watchNumber := t.watchCalls.Add(1)
	if t.disconnectFirstWatch && watchNumber == 1 {
		response.Body = &disconnectingBody{ReadCloser: response.Body}
	}
	return response, nil
}

func (t *expiredTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Query().Get("watch") != "true" {
		t.listCalls.Add(1)
		return t.base.RoundTrip(req)
	}
	t.mu.Lock()
	t.watchVersions = append(t.watchVersions, req.URL.Query().Get("resourceVersion"))
	t.mu.Unlock()
	watchNumber := t.watchCalls.Add(1)
	if watchNumber == 1 {
		payload, err := json.Marshal(metav1.Status{
			Status:  metav1.StatusFailure,
			Reason:  metav1.StatusReasonExpired,
			Details: &metav1.StatusDetails{RetryAfterSeconds: 0},
			Code:    http.StatusGone,
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusGone,
			Status:     "410 Gone",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(string(payload))),
			Request:    req,
		}, nil
	}
	return t.base.RoundTrip(req)
}

func (t *disconnectingTransport) resourceVersions() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.watchVersions...)
}

func (t *expiredTransport) resourceVersions() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.watchVersions...)
}

type trackingTransport struct {
	base          http.RoundTripper
	activeWatches atomic.Int64
	watchCalls    atomic.Int64
}

func (t *trackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.URL.Query().Get("watch") == "true" {
		t.activeWatches.Add(1)
		t.watchCalls.Add(1)
		response.Body = &trackedBody{ReadCloser: response.Body, active: &t.activeWatches}
	}
	return response, nil
}

type disconnectingBody struct {
	io.ReadCloser
	once sync.Once
}

func (b *disconnectingBody) Read(_ []byte) (int, error) {
	b.once.Do(func() { _ = b.Close() })
	return 0, io.EOF
}

type trackedBody struct {
	io.ReadCloser
	active *atomic.Int64
	once   sync.Once
}

func (b *trackedBody) Close() error {
	var err error
	b.once.Do(func() {
		b.active.Add(-1)
		err = b.ReadCloser.Close()
	})
	return err
}

func assertResourceVersionsDoNotRegress(t *testing.T, versions []string) {
	t.Helper()
	require.GreaterOrEqual(t, len(versions), 2)
	parsed := make([]int64, 0, len(versions))
	for _, version := range versions {
		value, err := strconv.ParseInt(version, 10, 64)
		require.NoError(t, err)
		parsed = append(parsed, value)
	}
	for index := 1; index < len(parsed); index++ {
		assert.GreaterOrEqual(t, parsed[index], parsed[index-1])
	}
}

func integrationClient(t *testing.T) (*kubernetes.Clientset, *rest.Config) {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	require.NoError(t, err, "build integration kubeconfig")
	client, err := kubernetes.NewForConfig(config)
	require.NoError(t, err, "create integration client")
	_, err = client.Discovery().ServerVersion()
	require.NoError(t, err, "connect to integration API server")
	return client, config
}

func clientWithTransport(t *testing.T, config *rest.Config, transport interface {
	RoundTrip(*http.Request) (*http.Response, error)
}) *kubernetes.Clientset {
	t.Helper()
	wrappedConfig := rest.CopyConfig(config)
	wrappedConfig.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		switch wrapped := transport.(type) {
		case *disconnectingTransport:
			wrapped.base = base
		case *trackingTransport:
			wrapped.base = base
		case *expiredTransport:
			wrapped.base = base
		default:
			t.Fatalf("unsupported test transport %T", transport)
		}
		return transport
	}
	client, err := kubernetes.NewForConfig(wrappedConfig)
	require.NoError(t, err)
	return client
}

func createNamespace(t *testing.T, client kubernetes.Interface) string {
	t.Helper()
	namespace, err := client.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "rollout-watch-"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := client.CoreV1().Namespaces().Delete(ctx, namespace.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete namespace %s: %v", namespace.Name, err)
			return
		}
		assert.Eventually(t, func() bool {
			_, err := client.CoreV1().Namespaces().Get(ctx, namespace.Name, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, time.Minute, 100*time.Millisecond)
	})
	return namespace.Name
}

func createDeployment(t *testing.T, client kubernetes.Interface, namespace, name string) *appsv1.Deployment {
	t.Helper()
	deployment, err := client.AppsV1().Deployments(namespace).Create(t.Context(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: testLabels(name)},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: testLabels(name)},
			Template: podTemplate(name),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return deployment
}

func createStatefulSet(t *testing.T, client kubernetes.Interface, namespace, name string) *appsv1.StatefulSet {
	t.Helper()
	_, err := client.CoreV1().Services(namespace).Create(t.Context(), &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: testLabels(name)},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  testLabels(name),
			Ports:     []corev1.ServicePort{{Name: "http", Port: 80}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	statefulSet, err := client.AppsV1().StatefulSets(namespace).Create(t.Context(), &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: testLabels(name)},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: name,
			Replicas:    ptr.To(int32(1)),
			Selector:    &metav1.LabelSelector{MatchLabels: testLabels(name)},
			Template:    podTemplate(name),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return statefulSet
}

func createDaemonSet(t *testing.T, client kubernetes.Interface, namespace, name string) *appsv1.DaemonSet {
	t.Helper()
	daemonSet, err := client.AppsV1().DaemonSets(namespace).Create(t.Context(), &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: testLabels(name)},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: testLabels(name)},
			Template: podTemplate(name),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return daemonSet
}

func createJob(t *testing.T, client kubernetes.Interface, namespace, name string) *batchv1.Job {
	t.Helper()
	job, err := client.BatchV1().Jobs(namespace).Create(t.Context(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: testLabels(name)},
		Spec: batchv1.JobSpec{
			Template: jobPodTemplate(name),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return job
}

func updateDeploymentTemplateAnnotation(
	t *testing.T,
	client kubernetes.Interface,
	namespace string,
	name string,
	value string,
) (*appsv1.Deployment, error) {
	for range 10 {
		deployment, err := client.AppsV1().Deployments(namespace).Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		deployment.Spec.Template.Annotations = map[string]string{"rollout-watch": value}
		updated, err := client.AppsV1().Deployments(namespace).Update(t.Context(), deployment, metav1.UpdateOptions{})
		if err == nil {
			return updated, nil
		}
		if !apierrors.IsConflict(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("update deployment %s/%s after conflicts", namespace, name)
}

func podTemplate(name string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: testLabels(name)},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: name, Image: rolloutImage, ImagePullPolicy: corev1.PullIfNotPresent}},
		},
	}
}

func jobPodTemplate(name string) corev1.PodTemplateSpec {
	template := podTemplate(name)
	template.Spec.RestartPolicy = corev1.RestartPolicyNever
	template.Spec.Containers[0] = corev1.Container{Name: name, Image: jobImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"true"}}
	return template
}

func testLabels(name string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": fmt.Sprintf("rollout-watch-%s", name)}
}
