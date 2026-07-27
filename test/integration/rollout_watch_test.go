//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	"github.com/ndzuki/release-manager/internal/operator/observer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	defaultWorkloadImage = "busybox:1.36@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"
	defaultSceneTimeout  = 30 * time.Second
	defaultJobCommand    = "/bin/true"
	jobFailureDelay      = 10
)

// ─── Fixture ───────────────────────────────────────────────────────────────

type rolloutFixture struct {
	adminClient kubernetes.Interface
	adminConfig *rest.Config
	observerCfg *rest.Config
	namespace   string
	testID      string
	transport   *trackingTransport
}

func setupRolloutFixture(t *testing.T, testID string) *rolloutFixture {
	t.Helper()

	testID = normalizeTestID(testID)
	adminClient, adminCfg := integrationClient(t)
	namespace := createTestNamespace(t, adminClient, testID)

	saName := "observer"
	createRBAC(t, adminClient, namespace, saName)
	token := requestToken(t, adminClient, namespace, saName)

	transport := &trackingTransport{}
	obsCfg := rest.AnonymousClientConfig(adminCfg)
	obsCfg.BearerToken = token
	obsCfg.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		transport.base = base
		return transport
	}

	fixture := &rolloutFixture{
		adminClient: adminClient,
		adminConfig: adminCfg,
		observerCfg: obsCfg,
		namespace:   namespace,
		testID:      testID,
		transport:   transport,
	}
	t.Cleanup(func() { fixture.assertClean(t) })
	return fixture
}

func (f *rolloutFixture) createDeployment(t *testing.T, name string) *appsv1.Deployment {
	t.Helper()
	selector := testLabels(f.testID, name)
	d, err := f.adminClient.AppsV1().Deployments(f.namespace).Create(t.Context(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: selector},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: workloadPodTemplate(name, selector),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return d
}

func (f *rolloutFixture) createStatefulSet(t *testing.T, name string) *appsv1.StatefulSet {
	t.Helper()
	ns := f.namespace
	selector := testLabels(f.testID, name)
	_, err := f.adminClient.CoreV1().Services(ns).Create(t.Context(), &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: selector},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  selector,
			Ports:     []corev1.ServicePort{{Name: "http", Port: 80}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	ss, err := f.adminClient.AppsV1().StatefulSets(ns).Create(t.Context(), &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: selector},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: name,
			Replicas:    ptr.To(int32(1)),
			Selector:    &metav1.LabelSelector{MatchLabels: selector},
			Template:    workloadPodTemplate(name, selector),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return ss
}

func (f *rolloutFixture) createDaemonSet(t *testing.T, name string) *appsv1.DaemonSet {
	t.Helper()
	selector := testLabels(f.testID, name)
	ds, err := f.adminClient.AppsV1().DaemonSets(f.namespace).Create(t.Context(), &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: selector},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: workloadPodTemplate(name, selector),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return ds
}

func (f *rolloutFixture) createJob(t *testing.T, name string) *batchv1.Job {
	t.Helper()
	selector := testLabels(f.testID, name)
	template := workloadPodTemplate(name, selector)
	template.Spec.RestartPolicy = corev1.RestartPolicyNever
	template.Spec.Containers[0].Command = []string{defaultJobCommand}
	job, err := f.adminClient.BatchV1().Jobs(f.namespace).Create(t.Context(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: selector},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To[int32](jobFailureDelay),
			Template:     template,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return job
}
func (f *rolloutFixture) createPendingJob(t *testing.T, name string) *batchv1.Job {
	t.Helper()
	selector := testLabels(f.testID, name)
	template := workloadPodTemplate(name, selector)
	template.Spec.RestartPolicy = corev1.RestartPolicyNever
	template.Spec.Containers[0].Command = []string{"/bin/sh", "-c", "sleep 3600"}
	job, err := f.adminClient.BatchV1().Jobs(f.namespace).Create(t.Context(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: selector},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To[int32](jobFailureDelay),
			Template:     template,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return job
}

func (f *rolloutFixture) observerClient(t *testing.T) *kubernetes.Clientset {
	t.Helper()
	client, err := kubernetes.NewForConfig(f.observerCfg)
	require.NoError(t, err)
	return client
}

func (f *rolloutFixture) deploymentRef(name string) observer.ResourceRef {
	return observer.ResourceRef{GVR: observer.DeploymentGVR, Namespace: f.namespace, Name: name}
}

func (f *rolloutFixture) statefulSetRef(name string) observer.ResourceRef {
	return observer.ResourceRef{GVR: observer.StatefulSetGVR, Namespace: f.namespace, Name: name}
}

func (f *rolloutFixture) daemonSetRef(name string) observer.ResourceRef {
	return observer.ResourceRef{GVR: observer.DaemonSetGVR, Namespace: f.namespace, Name: name}
}

func (f *rolloutFixture) jobRef(name string) observer.ResourceRef {
	return observer.ResourceRef{GVR: observer.JobGVR, Namespace: f.namespace, Name: name}
}

func (f *rolloutFixture) assertNoSecrets(t *testing.T) {
	t.Helper()
	assert.Equal(t, int64(0), f.transport.secretCalls.Load(),
		"observer must not access Secret API")
}

func (f *rolloutFixture) assertClean(t *testing.T) {
	t.Helper()
	f.assertNoSecrets(t)
	assert.Eventually(t, func() bool { return f.transport.activeWatches.Load() == 0 }, 5*time.Second, 100*time.Millisecond,
		"active watch requests did not return to zero")
}

// ─── RBAC ──────────────────────────────────────────────────────────────────

func createRBAC(t *testing.T, client kubernetes.Interface, namespace, saName string) {
	t.Helper()
	_, err := client.CoreV1().ServiceAccounts(namespace).Create(t.Context(), &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	roleName := saName
	_, err = client.RbacV1().Roles(namespace).Create(t.Context(), &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: roleName},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"apps"},
				Resources: []string{"deployments", "statefulsets", "daemonsets"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = client.RbacV1().RoleBindings(namespace).Create(t.Context(), &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: roleName},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: saName, Namespace: namespace},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

func requestToken(t *testing.T, client kubernetes.Interface, namespace, saName string) string {
	t.Helper()
	expiration := int64(600)
	tr, err := client.CoreV1().ServiceAccounts(namespace).CreateToken(t.Context(), saName,
		&authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				ExpirationSeconds: &expiration,
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, tr.Status.Token)
	return tr.Status.Token
}

// ─── Namespace ─────────────────────────────────────────────────────────────

func createTestNamespace(t *testing.T, client kubernetes.Interface, testID string) string {
	t.Helper()
	name := "rm-rollout-watch-" + testID
	_, err := client.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: testLabels(testID, "namespace"),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := deleteTestResources(ctx, client, testID); err != nil {
			t.Errorf("delete resources for test-id %s: %v", testID, err)
			return
		}
		grace := int64(0)
		propagation := metav1.DeletePropagationBackground
		if err := client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{
			GracePeriodSeconds: &grace,
			PropagationPolicy:  &propagation,
		}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete namespace %s: %v", name, err)
			return
		}

		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			_, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return
			}
			if err != nil && ctx.Err() == nil {
				t.Errorf("get namespace %s during cleanup: %v", name, err)
				return
			}

			gone, listErr := testWorkloadsGone(ctx, client, testID)
			if listErr == nil && gone {
				return
			}
			if listErr != nil && ctx.Err() == nil {
				t.Errorf("list workloads for test-id %s during cleanup: %v", testID, listErr)
				return
			}

			select {
			case <-ctx.Done():
				t.Errorf("namespace %s and labeled workloads were not cleaned before deadline", name)
				return
			case <-ticker.C:
			}
		}
	})
	return name
}

func deleteTestResources(ctx context.Context, client kubernetes.Interface, testID string) error {
	selector := fmt.Sprintf("release-manager.io/test-id=%s", testID)
	grace := int64(0)
	deleteOptions := metav1.DeleteOptions{
		GracePeriodSeconds: &grace,
		PropagationPolicy:  ptr.To(metav1.DeletePropagationBackground),
	}
	listOptions := metav1.ListOptions{LabelSelector: selector}

	deployments, err := client.AppsV1().Deployments("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list deployments: %w", err)
	}
	for index := range deployments.Items {
		item := &deployments.Items[index]
		if err := client.AppsV1().Deployments(item.Namespace).Delete(ctx, item.Name, deleteOptions); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete deployment %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	statefulSets, err := client.AppsV1().StatefulSets("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list statefulsets: %w", err)
	}
	for index := range statefulSets.Items {
		item := &statefulSets.Items[index]
		if err := client.AppsV1().StatefulSets(item.Namespace).Delete(ctx, item.Name, deleteOptions); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete statefulset %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	daemonSets, err := client.AppsV1().DaemonSets("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list daemonsets: %w", err)
	}
	for index := range daemonSets.Items {
		item := &daemonSets.Items[index]
		if err := client.AppsV1().DaemonSets(item.Namespace).Delete(ctx, item.Name, deleteOptions); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete daemonset %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	jobs, err := client.BatchV1().Jobs("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}
	for index := range jobs.Items {
		item := &jobs.Items[index]
		if err := client.BatchV1().Jobs(item.Namespace).Delete(ctx, item.Name, deleteOptions); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete job %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	pods, err := client.CoreV1().Pods("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	for index := range pods.Items {
		item := &pods.Items[index]
		if err := client.CoreV1().Pods(item.Namespace).Delete(ctx, item.Name, deleteOptions); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete pod %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	services, err := client.CoreV1().Services("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	for index := range services.Items {
		item := &services.Items[index]
		if err := client.CoreV1().Services(item.Namespace).Delete(ctx, item.Name, deleteOptions); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete service %s/%s: %w", item.Namespace, item.Name, err)
		}
	}
	return nil
}

func testWorkloadsGone(ctx context.Context, client kubernetes.Interface, testID string) (bool, error) {
	selector := fmt.Sprintf("release-manager.io/test-id=%s", testID)
	deployments, err := client.AppsV1().Deployments("").List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, fmt.Errorf("list deployments: %w", err)
	}
	statefulSets, err := client.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, fmt.Errorf("list statefulsets: %w", err)
	}
	daemonSets, err := client.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, fmt.Errorf("list daemonsets: %w", err)
	}
	jobs, err := client.BatchV1().Jobs("").List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, fmt.Errorf("list jobs: %w", err)
	}
	return len(deployments.Items) == 0 &&
		len(statefulSets.Items) == 0 &&
		len(daemonSets.Items) == 0 &&
		len(jobs.Items) == 0, nil
}

// ─── Transport ─────────────────────────────────────────────────────────────

type trackingTransport struct {
	base          http.RoundTripper
	activeWatches atomic.Int64
	watchCalls    atomic.Int64
	listCalls     atomic.Int64
	secretCalls   atomic.Int64
}

func (t *trackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if isSecretReq(req) {
		t.secretCalls.Add(1)
	}
	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.URL.Query().Get("watch") == "true" {
		t.activeWatches.Add(1)
		t.watchCalls.Add(1)
		response.Body = &trackedBody{ReadCloser: response.Body, active: &t.activeWatches}
	} else if !isSecretReq(req) {
		t.listCalls.Add(1)
	}
	return response, nil
}

func (t *trackingTransport) setBase(base http.RoundTripper) {
	t.base = base
}

func isSecretReq(req *http.Request) bool {
	return strings.Contains(req.URL.Path, "/secrets")
}

type disconnectingTransport struct {
	trackingTransport
	disconnectFirstWatch bool
	mu                   sync.Mutex
	watchVersions        []string
}

func (t *disconnectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.trackingTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.URL.Query().Get("watch") != "true" {
		return response, nil
	}
	t.mu.Lock()
	t.watchVersions = append(t.watchVersions, req.URL.Query().Get("resourceVersion"))
	watchNum := t.watchCalls.Load()
	t.mu.Unlock()
	if t.disconnectFirstWatch && watchNum == 1 {
		response.Body = &disconnectingBody{ReadCloser: response.Body}
	}
	return response, nil
}

func (t *disconnectingTransport) setBase(base http.RoundTripper) {
	t.trackingTransport.setBase(base)
}

func (t *disconnectingTransport) resourceVersions() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.watchVersions)
}

type expiredTransport struct {
	trackingTransport
	mu            sync.Mutex
	watchVersions []string
}

func (t *expiredTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Query().Get("watch") != "true" {
		return t.trackingTransport.RoundTrip(req)
	}
	t.mu.Lock()
	t.watchVersions = append(t.watchVersions, req.URL.Query().Get("resourceVersion"))
	watchNum := t.watchCalls.Add(1)
	t.mu.Unlock()
	if watchNum == 1 {
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
	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	t.activeWatches.Add(1)
	response.Body = &trackedBody{ReadCloser: response.Body, active: &t.activeWatches}
	return response, nil
}

func (t *expiredTransport) setBase(base http.RoundTripper) {
	t.trackingTransport.setBase(base)
}

func (t *expiredTransport) resourceVersions() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.watchVersions)
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

// ─── Helpers ───────────────────────────────────────────────────────────────

func integrationClient(t *testing.T) (*kubernetes.Clientset, *rest.Config) {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	require.NoError(t, err, "build integration kubeconfig")
	config.QPS = 100
	config.Burst = 200
	client, err := kubernetes.NewForConfig(config)
	require.NoError(t, err, "create integration client")
	_, err = client.Discovery().ServerVersion()
	require.NoError(t, err, "connect to integration API server")
	return client, config
}

type roundTripperWrapper interface {
	http.RoundTripper
	setBase(http.RoundTripper)
}

func transportWrapper(config *rest.Config, transport roundTripperWrapper) *rest.Config {
	wrapper := rest.CopyConfig(config)
	wrapper.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		transport.setBase(base)
		return transport
	}
	return wrapper
}

func clientWithDisconnectingTransport(t *testing.T, config *rest.Config, transport *disconnectingTransport) *kubernetes.Clientset {
	t.Helper()
	wrapper := transportWrapper(config, transport)
	client, err := kubernetes.NewForConfig(wrapper)
	require.NoError(t, err)
	return client
}

func clientWithExpiredTransport(t *testing.T, config *rest.Config, transport *expiredTransport) *kubernetes.Clientset {
	t.Helper()
	wrapper := transportWrapper(config, transport)
	client, err := kubernetes.NewForConfig(wrapper)
	require.NoError(t, err)
	return client
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

func updateDeploymentTemplateAnnotation(
	t *testing.T,
	client kubernetes.Interface,
	namespace, name, value string,
) (*appsv1.Deployment, error) {
	var lastErr error
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
		lastErr = err
	}
	return nil, fmt.Errorf("update deployment %s/%s after conflicts: %w", namespace, name, lastErr)
}

func updateDeploymentStatus(
	t *testing.T,
	client kubernetes.Interface,
	namespace, name string,
	mutate func(*appsv1.Deployment),
) error {
	t.Helper()
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := client.AppsV1().Deployments(namespace).Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		mutate(deployment)
		_, err = client.AppsV1().Deployments(namespace).UpdateStatus(t.Context(), deployment, metav1.UpdateOptions{})
		return err
	})
}

func updateJobStatus(
	t *testing.T,
	client kubernetes.Interface,
	namespace, name string,
	mutate func(*batchv1.Job),
) error {
	t.Helper()
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		job, err := client.BatchV1().Jobs(namespace).Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		mutate(job)
		_, err = client.BatchV1().Jobs(namespace).UpdateStatus(t.Context(), job, metav1.UpdateOptions{})
		return err
	})
}

func assertRolloutLast(t *testing.T, result observer.WatchResult, err error) {
	t.Helper()
	var rolloutErr *observer.RolloutError
	require.ErrorAs(t, err, &rolloutErr)
	assert.Equal(t, result, rolloutErr.Last)
}

func workloadPodTemplate(name string, selector map[string]string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: selector},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:            name,
				Image:           defaultWorkloadImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"/bin/sh", "-c", "sleep 3600"},
			}},
		},
	}
}

func normalizeTestID(name string) string {
	var normalized strings.Builder
	normalized.Grow(len(name))
	separator := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			normalized.WriteRune(r)
			separator = false
		} else if normalized.Len() > 0 && !separator {
			normalized.WriteByte('-')
			separator = true
		}
	}
	result := strings.Trim(normalized.String(), "-")
	if len(result) > 40 {
		result = strings.TrimRight(result[:40], "-")
	}
	if result == "" {
		return "case"
	}
	return result
}

func testLabels(testID, workload string) map[string]string {
	return map[string]string{
		"release-manager.io/test-id": testID,
		"app.kubernetes.io/name":     workload,
	}
}

// ─── Test Matrix ───────────────────────────────────────────────────────────

func TestRolloutWatchReadyWorkloads(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	obs := observer.New(fx.observerClient(t))

	deployment := fx.createDeployment(t, "deployment")
	statefulSet := fx.createStatefulSet(t, "statefulset")
	daemonSet := fx.createDaemonSet(t, "daemonset")
	job := fx.createJob(t, "job")

	readyTests := []struct {
		name string
		ref  observer.ResourceRef
		gen  int64
	}{
		{name: "deployment", ref: fx.deploymentRef(deployment.Name), gen: deployment.Generation},
		{name: "statefulset", ref: fx.statefulSetRef(statefulSet.Name), gen: statefulSet.Generation},
		{name: "daemonset", ref: fx.daemonSetRef(daemonSet.Name), gen: daemonSet.Generation},
		{name: "job", ref: fx.jobRef(job.Name), gen: 0},
	}
	for _, test := range readyTests {
		t.Run(test.name, func(t *testing.T) {
			result, err := obs.Observe(t.Context(), test.ref, test.gen, defaultSceneTimeout)
			require.NoError(t, err)
			assert.True(t, result.Ready)
			assert.False(t, result.Failed)
			assert.NotEmpty(t, result.ResourceUID)
			assert.NotEmpty(t, result.ResourceVersion)
		})
	}

	fx.assertClean(t)
}

func TestRolloutWatchAppsObservedGenerationGate(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	obs := observer.New(fx.observerClient(t))
	deployment := fx.createDeployment(t, "deployment")

	resultCh := make(chan observer.WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := obs.Observe(t.Context(), fx.deploymentRef(deployment.Name), deployment.Generation+1, defaultSceneTimeout)
		resultCh <- result
		errCh <- err
	}()
	require.Eventually(t, func() bool { return fx.transport.activeWatches.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
	updated, err := updateDeploymentTemplateAnnotation(t, fx.adminClient, fx.namespace, deployment.Name, "next-generation")
	require.NoError(t, err)
	result := <-resultCh
	err = <-errCh
	require.NoError(t, err)
	assert.True(t, result.Ready)
	assert.GreaterOrEqual(t, result.Generation, updated.Generation)
	assert.GreaterOrEqual(t, result.ObservedGeneration, updated.Generation)

	fx.assertClean(t)
}
func TestRolloutWatchValidatesBeforeAPI(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	client := fx.observerClient(t)
	obs := observer.New(client)
	unsupported := observer.ResourceRef{
		GVR:       schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"},
		Namespace: fx.namespace,
		Name:      "widget",
	}
	tests := []struct {
		name       string
		ref        observer.ResourceRef
		generation int64
		timeout    time.Duration
		wantCode   observer.ErrorCode
	}{
		{name: "empty namespace", ref: observer.ResourceRef{GVR: observer.DeploymentGVR, Name: "deployment"}, generation: 1, timeout: time.Second, wantCode: observer.ErrorCodeInvalidArgument},
		{name: "empty name", ref: observer.ResourceRef{GVR: observer.DeploymentGVR, Namespace: fx.namespace}, generation: 1, timeout: time.Second, wantCode: observer.ErrorCodeInvalidArgument},
		{name: "invalid apps generation", ref: fx.deploymentRef("deployment"), generation: 0, timeout: time.Second, wantCode: observer.ErrorCodeInvalidArgument},
		{name: "invalid job generation", ref: fx.jobRef("job"), generation: 1, timeout: time.Second, wantCode: observer.ErrorCodeInvalidArgument},
		{name: "invalid timeout", ref: fx.deploymentRef("deployment"), generation: 1, timeout: 0, wantCode: observer.ErrorCodeInvalidArgument},
		{name: "unsupported resource", ref: unsupported, generation: 0, timeout: time.Second, wantCode: observer.ErrorCodeUnsupportedResource},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeLists := fx.transport.listCalls.Load()
			beforeWatches := fx.transport.watchCalls.Load()
			result, err := obs.Observe(t.Context(), test.ref, test.generation, test.timeout)
			require.Error(t, err)
			assert.Equal(t, test.wantCode, observerCode(t, err))
			assert.Equal(t, test.ref, result.Resource)
			assertRolloutLast(t, result, err)
			assert.Equal(t, beforeLists, fx.transport.listCalls.Load())
			assert.Equal(t, beforeWatches, fx.transport.watchCalls.Load())
		})
	}
	fx.assertClean(t)
}

func TestRolloutWatchWorkloadFailures(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	obs := observer.New(fx.observerClient(t))

	t.Run("deployment terminal failure", func(t *testing.T) {
		deployment := fx.createDeployment(t, "deployment-failed")
		resultCh := make(chan observer.WatchResult, 1)
		errCh := make(chan error, 1)
		go func() {
			result, err := obs.Observe(t.Context(), fx.deploymentRef(deployment.Name), deployment.Generation+1, defaultSceneTimeout)
			resultCh <- result
			errCh <- err
		}()
		require.Eventually(t, func() bool { return fx.transport.activeWatches.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
		err := updateDeploymentStatus(t, fx.adminClient, fx.namespace, deployment.Name, func(current *appsv1.Deployment) {
			current.Status.Conditions = []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate",
			}}
		})
		require.NoError(t, err)
		result := <-resultCh
		err = <-errCh
		assert.ErrorIs(t, err, observer.ErrWorkloadUnavailable)
		assert.True(t, result.Failed)
		assertRolloutLast(t, result, err)
	})

	t.Run("job terminal failure", func(t *testing.T) {
		job := fx.createPendingJob(t, "job-failed")
		resultCh := make(chan observer.WatchResult, 1)
		errCh := make(chan error, 1)
		go func() {
			result, err := obs.Observe(t.Context(), fx.jobRef(job.Name), 0, defaultSceneTimeout)
			resultCh <- result
			errCh <- err
		}()
		require.Eventually(t, func() bool { return fx.transport.activeWatches.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
		err := updateJobStatus(t, fx.adminClient, fx.namespace, job.Name, func(current *batchv1.Job) {
			current.Status.Active = 0
			current.Status.Conditions = []batchv1.JobCondition{
				{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
			}
		})
		require.NoError(t, err)
		result := <-resultCh
		err = <-errCh
		assert.ErrorIs(t, err, observer.ErrWorkloadUnavailable)
		assert.True(t, result.Failed)
		assertRolloutLast(t, result, err)
	})

	t.Run("statefulset non-terminal condition does not fail", func(t *testing.T) {
		ss := fx.createStatefulSet(t, "ss-pending")
		result, err := obs.Observe(t.Context(), fx.statefulSetRef(ss.Name), ss.Generation+100, time.Second)
		assert.ErrorIs(t, err, observer.ErrRolloutTimeout)
		assert.False(t, errors.Is(err, observer.ErrWorkloadUnavailable))
		assertRolloutLast(t, result, err)
	})

	t.Run("daemonset non-terminal condition does not fail", func(t *testing.T) {
		ds := fx.createDaemonSet(t, "ds-pending")
		result, err := obs.Observe(t.Context(), fx.daemonSetRef(ds.Name), ds.Generation+100, time.Second)
		assert.ErrorIs(t, err, observer.ErrRolloutTimeout)
		assert.False(t, errors.Is(err, observer.ErrWorkloadUnavailable))
		assertRolloutLast(t, result, err)
	})
}

func TestRolloutWatchTransportDisconnect(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	deployment := fx.createDeployment(t, "reconnect")
	ref := fx.deploymentRef(deployment.Name)

	transport := &disconnectingTransport{disconnectFirstWatch: true}
	client := clientWithDisconnectingTransport(t, fx.observerCfg, transport)
	obs := observer.New(client)

	resultCh := make(chan observer.WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := obs.Observe(t.Context(), ref, deployment.Generation+1, defaultSceneTimeout)
		resultCh <- result
		errCh <- err
	}()

	require.Eventually(t, func() bool { return transport.watchCalls.Load() >= 1 }, 5*time.Second, 10*time.Millisecond)
	updated, err := updateDeploymentTemplateAnnotation(t, fx.adminClient, fx.namespace, deployment.Name, "ready")
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
	case <-time.After(defaultSceneTimeout):
		t.Fatal("observer did not recover after transport disconnect")
	}

	fx.assertClean(t)
}

func TestRolloutWatchResourceVersionExpired(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	deployment := fx.createDeployment(t, "expired")
	ref := fx.deploymentRef(deployment.Name)

	transport := &expiredTransport{}
	client := clientWithExpiredTransport(t, fx.observerCfg, transport)
	obs := observer.New(client)

	resultCh := make(chan observer.WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := obs.Observe(t.Context(), ref, deployment.Generation+1, defaultSceneTimeout)
		resultCh <- result
		errCh <- err
	}()

	require.Eventually(t, func() bool { return transport.watchCalls.Load() >= 2 }, 5*time.Second, 10*time.Millisecond)
	updated, err := updateDeploymentTemplateAnnotation(t, fx.adminClient, fx.namespace, deployment.Name, "expired-recovery")
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
	case <-time.After(defaultSceneTimeout):
		t.Fatal("observer did not recover after resource version expiration")
	}

	fx.assertClean(t)
}

func TestRolloutWatchTimeout(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	deployment := fx.createDeployment(t, "timeout")

	result, err := observer.New(fx.observerClient(t)).Observe(
		t.Context(), fx.deploymentRef(deployment.Name), deployment.Generation+100, 1*time.Second,
	)

	assert.ErrorIs(t, err, observer.ErrRolloutTimeout)
	assert.False(t, result.Ready)
	assertRolloutLast(t, result, err)
	fx.assertClean(t)
}

func TestRolloutWatchParentCancelWinsTimeout(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	deployment := fx.createDeployment(t, "cancel")

	for i := range 20 {
		t.Run(fmt.Sprintf("trial-%d", i), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			transport := &trackingTransport{}
			obsCfg := &rest.Config{}
			*obsCfg = *fx.observerCfg
			obsCfg.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
				transport.base = base
				return transport
			}
			client, err := kubernetes.NewForConfig(obsCfg)
			require.NoError(t, err)

			resultCh := make(chan observer.WatchResult, 1)
			errCh := make(chan error, 1)
			go func() {
				result, err := observer.New(client).Observe(ctx, fx.deploymentRef(deployment.Name), deployment.Generation+100, 3*time.Second)
				resultCh <- result
				errCh <- err
			}()

			require.Eventually(t, func() bool { return transport.activeWatches.Load() == 1 }, time.Second, 10*time.Millisecond)
			cancel()

			select {
			case err := <-errCh:
				result := <-resultCh
				assert.ErrorIs(t, err, observer.ErrCancelled)
				assert.Equal(t, observer.ErrorCodeCancelled, observerCode(t, err))
				assertRolloutLast(t, result, err)
			case <-time.After(5 * time.Second):
				t.Fatal("observer did not stop after parent cancellation")
			}
			assert.Eventually(t, func() bool { return transport.activeWatches.Load() == 0 }, 5*time.Second, 100*time.Millisecond)
		})
	}

	fx.assertClean(t)
}

func TestRolloutWatchDeleteAndReplace(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	obs := observer.New(fx.observerClient(t))

	t.Run("deleted object returns workload_unavailable", func(t *testing.T) {
		deployment := fx.createDeployment(t, "delete")
		ref := fx.deploymentRef(deployment.Name)
		resultCh := make(chan observer.WatchResult, 1)
		errCh := make(chan error, 1)
		go func() {
			result, err := obs.Observe(t.Context(), ref, deployment.Generation+1, defaultSceneTimeout)
			resultCh <- result
			errCh <- err
		}()
		require.Eventually(t, func() bool { return fx.transport.activeWatches.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
		require.NoError(t, fx.adminClient.AppsV1().Deployments(fx.namespace).Delete(t.Context(), deployment.Name, metav1.DeleteOptions{}))
		result := <-resultCh
		err := <-errCh
		assert.ErrorIs(t, err, observer.ErrWorkloadUnavailable)
		assert.Equal(t, deployment.UID, result.ResourceUID)
		assertRolloutLast(t, result, err)
	})

	t.Run("replacement UID is rejected", func(t *testing.T) {
		deployment := fx.createDeployment(t, "replace")
		ref := fx.deploymentRef(deployment.Name)
		resultCh := make(chan observer.WatchResult, 1)
		errCh := make(chan error, 1)
		go func() {
			result, err := obs.Observe(t.Context(), ref, deployment.Generation+1, defaultSceneTimeout)
			resultCh <- result
			errCh <- err
		}()
		require.Eventually(t, func() bool { return fx.transport.activeWatches.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
		propagation := metav1.DeletePropagationForeground
		require.NoError(t, fx.adminClient.AppsV1().Deployments(fx.namespace).Delete(t.Context(), deployment.Name, metav1.DeleteOptions{PropagationPolicy: &propagation}))
		require.Eventually(t, func() bool {
			_, err := fx.adminClient.AppsV1().Deployments(fx.namespace).Get(t.Context(), deployment.Name, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 10*time.Second, 100*time.Millisecond)
		replacement := fx.createDeployment(t, deployment.Name)
		result := <-resultCh
		err := <-errCh
		assert.ErrorIs(t, err, observer.ErrWorkloadUnavailable)
		assert.Equal(t, deployment.UID, result.ResourceUID)
		assert.NotEqual(t, replacement.UID, result.ResourceUID)
		assertRolloutLast(t, result, err)
	})
}

func TestRolloutWatchRepeatedObserveIsIdempotent(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	obs := observer.New(fx.observerClient(t))
	deployment := fx.createDeployment(t, "repeat")
	ref := fx.deploymentRef(deployment.Name)

	first, err := obs.Observe(t.Context(), ref, deployment.Generation, defaultSceneTimeout)
	require.NoError(t, err)
	assert.True(t, first.Ready)

	second, err := obs.Observe(t.Context(), ref, deployment.Generation, defaultSceneTimeout)
	require.NoError(t, err)
	assert.True(t, second.Ready)
	assert.Equal(t, first.ResourceUID, second.ResourceUID)
	assert.Equal(t, first.Generation, second.Generation)

	fx.assertClean(t)
}

func TestRolloutWatchConcurrentIsolation(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	deployment := fx.createDeployment(t, "concurrent")
	ref := fx.deploymentRef(deployment.Name)

	ctx, cancel := context.WithCancel(t.Context())

	obs1 := observer.New(fx.observerClient(t))
	obs2 := observer.New(fx.observerClient(t))

	firstResultCh := make(chan observer.WatchResult, 1)
	firstErrCh := make(chan error, 1)
	secondResultCh := make(chan observer.WatchResult, 1)
	secondErrCh := make(chan error, 1)
	go func() {
		result, err := obs1.Observe(ctx, ref, deployment.Generation+100, defaultSceneTimeout)
		firstResultCh <- result
		firstErrCh <- err
	}()
	go func() {
		result, err := obs2.Observe(t.Context(), ref, deployment.Generation, defaultSceneTimeout)
		secondResultCh <- result
		secondErrCh <- err
	}()

	cancel()

	firstErr := <-firstErrCh
	firstResult := <-firstResultCh
	secondErr := <-secondErrCh
	secondResult := <-secondResultCh

	assert.ErrorIs(t, firstErr, observer.ErrCancelled)
	assert.False(t, firstResult.Ready)
	require.NoError(t, secondErr)
	assert.True(t, secondResult.Ready)

	fx.assertClean(t)
}

func TestRolloutWatchRBACSecretProbe(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	deployment := fx.createDeployment(t, "rbac")
	obs := observer.New(fx.observerClient(t))

	result, err := obs.Observe(t.Context(), fx.deploymentRef(deployment.Name), deployment.Generation, defaultSceneTimeout)
	require.NoError(t, err)
	assert.True(t, result.Ready)

	fx.assertNoSecrets(t)
	fx.assertClean(t)
}

func observerCode(t *testing.T, err error) observer.ErrorCode {
	t.Helper()
	var rolloutErr *observer.RolloutError
	require.ErrorAs(t, err, &rolloutErr)
	return rolloutErr.Code()
}
