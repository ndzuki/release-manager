//go:build integration

package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	"github.com/ndzuki/release-manager/internal/operator/observer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	workloadImageEnv    = "ROLLOUT_WATCH_WORKLOAD_IMAGE"
	defaultSceneTimeout = 30 * time.Second
	defaultJobCommand   = "/bin/true"
	jobFailureDelay     = 10
)

// ─── Fixture ───────────────────────────────────────────────────────────────

type rolloutFixture struct {
	adminClient   kubernetes.Interface
	observerCfg   *rest.Config
	namespace     string
	testID        string
	workloadImage string
	transport     *trackingTransport
}

func setupRolloutFixture(t *testing.T, testID string) *rolloutFixture {
	t.Helper()

	testID = normalizeTestID(testID)
	workloadImage := os.Getenv(workloadImageEnv)
	require.NotEmpty(t, workloadImage, workloadImageEnv+" must be set by make test-rollout-watch")
	adminClient, adminCfg := integrationClient(t)
	namespace := createTestNamespace(t, adminClient, testID)

	saName := "observer"
	createRBAC(t, adminClient, namespace, saName, testID)
	token := requestToken(t, adminClient, namespace, saName)

	transport := &trackingTransport{}
	obsCfg := rest.AnonymousClientConfig(adminCfg)
	obsCfg.ContentType = "application/json"
	obsCfg.AcceptContentTypes = "application/json"
	obsCfg.BearerToken = token
	obsCfg.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		transport.base = base
		return transport
	}

	fixture := &rolloutFixture{
		adminClient:   adminClient,
		observerCfg:   obsCfg,
		namespace:     namespace,
		testID:        testID,
		workloadImage: workloadImage,
		transport:     transport,
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
			Template: workloadPodTemplate(name, selector, f.workloadImage),
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
			Template:    workloadPodTemplate(name, selector, f.workloadImage),
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
			Template: workloadPodTemplate(name, selector, f.workloadImage),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return ds
}

func (f *rolloutFixture) createJob(t *testing.T, name string) *batchv1.Job {
	t.Helper()
	selector := testLabels(f.testID, name)
	template := workloadPodTemplate(name, selector, f.workloadImage)
	template.Spec.RestartPolicy = corev1.RestartPolicyNever
	template.Spec.Containers[0].Command = []string{defaultJobCommand}
	job, err := f.adminClient.BatchV1().Jobs(f.namespace).Create(t.Context(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: selector},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To[int32](jobFailureDelay),
			Suspend:      ptr.To(true),
			Template:     template,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	return job
}
func (f *rolloutFixture) createPendingJob(t *testing.T, name string) *batchv1.Job {
	t.Helper()
	selector := testLabels(f.testID, name)
	template := workloadPodTemplate(name, selector, f.workloadImage)
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
	assertTransportClean(t, f.transport)
}

func assertTransportClean(t *testing.T, transport *trackingTransport) {
	t.Helper()
	assert.Equal(t, int64(0), transport.secretCalls.Load(), "observer must not access Secret API")
	assert.Eventually(t, func() bool { return transport.activeWatches.Load() == 0 }, 5*time.Second, 100*time.Millisecond,
		"active watch requests did not return to zero")
}

// ─── RBAC ──────────────────────────────────────────────────────────────────

func createRBAC(t *testing.T, client kubernetes.Interface, namespace, saName, testID string) {
	t.Helper()
	labels := testLabels(testID, "observer-rbac")
	_, err := client.CoreV1().ServiceAccounts(namespace).Create(t.Context(), &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Labels: labels},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	roleName := saName
	_, err = client.RbacV1().Roles(namespace).Create(t.Context(), &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Labels: labels},
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
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Labels: labels},
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
		if err := cleanupTestNamespace(ctx, client, name, testID); err != nil {
			t.Errorf("cleanup namespace %s: %v", name, err)
		}
	})
	return name
}

func cleanupTestNamespace(
	ctx context.Context,
	client kubernetes.Interface,
	name, testID string,
) error {
	var cleanupErr error
	if err := deleteTestResources(ctx, client, testID); err != nil {
		cleanupErr = errors.Join(
			cleanupErr,
			fmt.Errorf("delete resources for test-id %s: %w", testID, err),
		)
	}

	grace := int64(0)
	propagation := metav1.DeletePropagationBackground
	if err := client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{
		GracePeriodSeconds: &grace,
		PropagationPolicy:  &propagation,
	}); err != nil && !apierrors.IsNotFound(err) {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete namespace %s: %w", name, err))
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var verificationErr error
		_, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return cleanupErr
		}
		if err != nil && ctx.Err() == nil {
			verificationErr = fmt.Errorf("get namespace %s during cleanup: %w", name, err)
		}

		gone, listErr := testResourcesGone(ctx, client, testID)
		if listErr == nil && gone {
			return cleanupErr
		}
		if listErr != nil && ctx.Err() == nil {
			verificationErr = errors.Join(
				verificationErr,
				fmt.Errorf("list resources for test-id %s during cleanup: %w", testID, listErr),
			)
		}

		select {
		case <-ctx.Done():
			return errors.Join(
				cleanupErr,
				verificationErr,
				fmt.Errorf("namespace %s was not deleted before deadline: %w", name, ctx.Err()),
			)
		case <-ticker.C:
		}
	}
}

func TestRolloutWatchCleanupDeletesNamespaceAfterResourceCleanupError(t *testing.T) {
	const (
		name   = "rm-rollout-watch-cleanup-error"
		testID = "cleanup-error"
	)
	client := kubernetesfake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: testLabels(testID, "namespace")},
	})
	client.PrependReactor("list", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("injected deployment list failure")
	})

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := cleanupTestNamespace(ctx, client, name, testID)

	require.ErrorContains(t, err, "delete resources for test-id cleanup-error")
	_, getErr := client.CoreV1().Namespaces().Get(t.Context(), name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(getErr), "namespace must be deleted after resource cleanup failure")
}

func TestRolloutWatchCleanupRetriesTransientVerificationError(t *testing.T) {
	const (
		name   = "rm-rollout-watch-cleanup-retry"
		testID = "cleanup-retry"
	)
	client := kubernetesfake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: testLabels(testID, "namespace")},
	})
	client.PrependReactor("delete", "namespaces", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})
	deploymentListCalls := 0
	client.PrependReactor("list", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		deploymentListCalls++
		if deploymentListCalls == 2 {
			return true, nil, errors.New("injected transient deployment list failure")
		}
		return false, nil, nil
	})

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := cleanupTestNamespace(ctx, client, name, testID)

	require.NoError(t, err)
	assert.GreaterOrEqual(t, deploymentListCalls, 3, "cleanup must retry resource verification")
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
		err := client.AppsV1().Deployments(item.Namespace).Delete(ctx, item.Name, deleteOptions)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete deployment %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	statefulSets, err := client.AppsV1().StatefulSets("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list statefulsets: %w", err)
	}
	for index := range statefulSets.Items {
		item := &statefulSets.Items[index]
		err := client.AppsV1().StatefulSets(item.Namespace).Delete(ctx, item.Name, deleteOptions)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete statefulset %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	daemonSets, err := client.AppsV1().DaemonSets("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list daemonsets: %w", err)
	}
	for index := range daemonSets.Items {
		item := &daemonSets.Items[index]
		err := client.AppsV1().DaemonSets(item.Namespace).Delete(ctx, item.Name, deleteOptions)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete daemonset %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	jobs, err := client.BatchV1().Jobs("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}
	for index := range jobs.Items {
		item := &jobs.Items[index]
		err := client.BatchV1().Jobs(item.Namespace).Delete(ctx, item.Name, deleteOptions)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete job %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	pods, err := client.CoreV1().Pods("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	for index := range pods.Items {
		item := &pods.Items[index]
		err := client.CoreV1().Pods(item.Namespace).Delete(ctx, item.Name, deleteOptions)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete pod %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	services, err := client.CoreV1().Services("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	for index := range services.Items {
		item := &services.Items[index]
		err := client.CoreV1().Services(item.Namespace).Delete(ctx, item.Name, deleteOptions)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete service %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	serviceAccounts, err := client.CoreV1().ServiceAccounts("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list serviceaccounts: %w", err)
	}
	for index := range serviceAccounts.Items {
		item := &serviceAccounts.Items[index]
		err := client.CoreV1().ServiceAccounts(item.Namespace).Delete(ctx, item.Name, deleteOptions)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete serviceaccount %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	roles, err := client.RbacV1().Roles("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list roles: %w", err)
	}
	for index := range roles.Items {
		item := &roles.Items[index]
		err := client.RbacV1().Roles(item.Namespace).Delete(ctx, item.Name, deleteOptions)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete role %s/%s: %w", item.Namespace, item.Name, err)
		}
	}

	roleBindings, err := client.RbacV1().RoleBindings("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("list rolebindings: %w", err)
	}
	for index := range roleBindings.Items {
		item := &roleBindings.Items[index]
		err := client.RbacV1().RoleBindings(item.Namespace).Delete(ctx, item.Name, deleteOptions)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete rolebinding %s/%s: %w", item.Namespace, item.Name, err)
		}
	}
	return nil
}

func testResourcesGone(ctx context.Context, client kubernetes.Interface, testID string) (bool, error) {
	selector := fmt.Sprintf("release-manager.io/test-id=%s", testID)
	listOptions := metav1.ListOptions{LabelSelector: selector}
	deployments, err := client.AppsV1().Deployments("").List(ctx, listOptions)
	if err != nil {
		return false, fmt.Errorf("list deployments: %w", err)
	}
	statefulSets, err := client.AppsV1().StatefulSets("").List(ctx, listOptions)
	if err != nil {
		return false, fmt.Errorf("list statefulsets: %w", err)
	}
	daemonSets, err := client.AppsV1().DaemonSets("").List(ctx, listOptions)
	if err != nil {
		return false, fmt.Errorf("list daemonsets: %w", err)
	}
	jobs, err := client.BatchV1().Jobs("").List(ctx, listOptions)
	if err != nil {
		return false, fmt.Errorf("list jobs: %w", err)
	}
	pods, err := client.CoreV1().Pods("").List(ctx, listOptions)
	if err != nil {
		return false, fmt.Errorf("list pods: %w", err)
	}
	services, err := client.CoreV1().Services("").List(ctx, listOptions)
	if err != nil {
		return false, fmt.Errorf("list services: %w", err)
	}
	serviceAccounts, err := client.CoreV1().ServiceAccounts("").List(ctx, listOptions)
	if err != nil {
		return false, fmt.Errorf("list serviceaccounts: %w", err)
	}
	roles, err := client.RbacV1().Roles("").List(ctx, listOptions)
	if err != nil {
		return false, fmt.Errorf("list roles: %w", err)
	}
	roleBindings, err := client.RbacV1().RoleBindings("").List(ctx, listOptions)
	if err != nil {
		return false, fmt.Errorf("list rolebindings: %w", err)
	}
	return len(deployments.Items) == 0 &&
		len(statefulSets.Items) == 0 &&
		len(daemonSets.Items) == 0 &&
		len(jobs.Items) == 0 &&
		len(pods.Items) == 0 &&
		len(services.Items) == 0 &&
		len(serviceAccounts.Items) == 0 &&
		len(roles.Items) == 0 &&
		len(roleBindings.Items) == 0, nil
}

// ─── Transport ─────────────────────────────────────────────────────────────

type trackingTransport struct {
	base          http.RoundTripper
	activeWatches atomic.Int64
	watchCalls    atomic.Int64
	listCalls     atomic.Int64
	secretCalls   atomic.Int64
	mu            sync.Mutex
	listVersions  []string
	listQueries   []string
	watchQueries  []string
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
		t.recordWatchQuery(req.URL.RawQuery)
		response.Body = &trackedBody{ReadCloser: response.Body, active: &t.activeWatches}
	} else if !isSecretReq(req) {
		t.listCalls.Add(1)
		t.recordListQuery(req.URL.RawQuery)
		if err := t.recordListResourceVersion(response); err != nil {
			return nil, err
		}
	}
	return response, nil
}

func (t *trackingTransport) setBase(base http.RoundTripper) {
	t.base = base
}

func (t *trackingTransport) recordListResourceVersion(response *http.Response) error {
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read list response: %w", err)
	}
	if err := response.Body.Close(); err != nil {
		return fmt.Errorf("close list response: %w", err)
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		return nil
	}
	var list struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(payload, &list); err != nil {
		return fmt.Errorf("decode list response: %w", err)
	}
	if list.Metadata.ResourceVersion != "" {
		t.mu.Lock()
		t.listVersions = append(t.listVersions, list.Metadata.ResourceVersion)
		t.mu.Unlock()
	}
	return nil
}

func (t *trackingTransport) listResourceVersions() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.listVersions)
}

func (t *trackingTransport) recordListQuery(rawQuery string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.listQueries = append(t.listQueries, rawQuery)
}

func (t *trackingTransport) recordWatchQuery(rawQuery string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.watchQueries = append(t.watchQueries, rawQuery)
}

func (t *trackingTransport) queries() (listQueries, watchQueries []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.listQueries), slices.Clone(t.watchQueries)
}

type watchEventBarrierTransport struct {
	trackingTransport
	marker      []byte
	matched     chan struct{}
	waiting     chan struct{}
	releaseRead chan struct{}
	releaseOnce sync.Once
}

func newWatchEventBarrierTransport(marker string) *watchEventBarrierTransport {
	return &watchEventBarrierTransport{
		marker:      []byte(marker),
		matched:     make(chan struct{}),
		waiting:     make(chan struct{}),
		releaseRead: make(chan struct{}),
	}
}

func (t *watchEventBarrierTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.trackingTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.URL.Query().Get("watch") != "true" {
		return response, nil
	}
	response.Body = newWatchEventBarrierBody(
		req.Context(),
		response.Body,
		t.marker,
		t.matched,
		t.waiting,
		t.releaseRead,
	)
	return response, nil
}

func (t *watchEventBarrierTransport) setBase(base http.RoundTripper) {
	t.trackingTransport.setBase(base)
}

func (t *watchEventBarrierTransport) release() {
	t.releaseOnce.Do(func() { close(t.releaseRead) })
}

// watchEventBarrierBody forwards complete watch-event lines, then blocks after
// the configured marker until the observer closes the response body.
type watchEventBarrierBody struct {
	source          io.ReadCloser
	reader          *bufio.Reader
	ctx             context.Context
	marker          []byte
	matched         chan struct{}
	waiting         chan struct{}
	releaseRead     <-chan struct{}
	closed          chan struct{}
	pending         []byte
	pendingErr      error
	hasPendingBlock bool
	isBlocked       bool
	matchOnce       sync.Once
	waitOnce        sync.Once
	closeOnce       sync.Once
}

func newWatchEventBarrierBody(
	ctx context.Context,
	source io.ReadCloser,
	marker []byte,
	matched chan struct{},
	waiting chan struct{},
	releaseRead <-chan struct{},
) *watchEventBarrierBody {
	return &watchEventBarrierBody{
		source:      source,
		reader:      bufio.NewReader(source),
		ctx:         ctx,
		marker:      marker,
		matched:     matched,
		waiting:     waiting,
		releaseRead: releaseRead,
		closed:      make(chan struct{}),
	}
}

func (b *watchEventBarrierBody) Read(p []byte) (int, error) {
	for len(b.pending) == 0 {
		if b.isBlocked {
			b.waitOnce.Do(func() { close(b.waiting) })
			if b.pendingErr != nil {
				err := b.pendingErr
				b.pendingErr = nil
				return 0, err
			}
			select {
			case <-b.ctx.Done():
				return 0, b.ctx.Err()
			case <-b.closed:
				return 0, io.EOF
			case <-b.releaseRead:
				b.isBlocked = false
				continue
			}
		}
		if b.pendingErr != nil {
			err := b.pendingErr
			b.pendingErr = nil
			return 0, err
		}
		line, err := b.reader.ReadBytes('\n')
		if len(line) == 0 {
			return 0, err
		}
		b.pending = line
		b.pendingErr = err
		b.hasPendingBlock = bytes.Contains(line, b.marker)
	}

	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	if len(b.pending) == 0 && b.hasPendingBlock {
		b.hasPendingBlock = false
		b.isBlocked = true
		b.matchOnce.Do(func() { close(b.matched) })
	}
	return n, nil
}

func (b *watchEventBarrierBody) Close() error {
	var err error
	b.closeOnce.Do(func() {
		close(b.closed)
		err = b.source.Close()
	})
	return err
}

func isSecretReq(req *http.Request) bool {
	return strings.Contains(req.URL.Path, "/secrets")
}

type disconnectingTransport struct {
	trackingTransport
	isFirstWatchDisconnectEnabled bool
	disconnectOnce                sync.Once
	secondListOnce                sync.Once
	releaseSecondListOnce         sync.Once
	disconnected                  chan struct{}
	secondListReady               chan struct{}
	releaseSecondList             chan struct{}
	mu                            sync.Mutex
	watchVersions                 []string
}

func (t *disconnectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.trackingTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.URL.Query().Get("watch") != "true" {
		if t.listCalls.Load() == 2 && t.secondListReady != nil {
			t.secondListOnce.Do(func() { close(t.secondListReady) })
			select {
			case <-t.releaseSecondList:
			case <-req.Context().Done():
				_ = response.Body.Close()
				return nil, req.Context().Err()
			}
		}
		return response, nil
	}
	t.mu.Lock()
	t.watchVersions = append(t.watchVersions, req.URL.Query().Get("resourceVersion"))
	watchNum := len(t.watchVersions)
	t.mu.Unlock()
	if t.isFirstWatchDisconnectEnabled && watchNum == 1 {
		disconnected := t.disconnected
		response.Body = &disconnectingBody{
			ReadCloser: response.Body,
			onDisconnect: func() {
				if disconnected != nil {
					t.disconnectOnce.Do(func() { close(disconnected) })
				}
			},
		}
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

func (t *disconnectingTransport) releaseRelist() {
	if t.releaseSecondList != nil {
		t.releaseSecondListOnce.Do(func() { close(t.releaseSecondList) })
	}
}

type relistBarrierTransport struct {
	trackingTransport
	listRequests   atomic.Int64
	disconnectOnce sync.Once
	relistOnce     sync.Once
	releaseOnce    sync.Once
	relistStarted  chan struct{}
	releaseRelist  chan struct{}
}

func newRelistBarrierTransport() *relistBarrierTransport {
	return &relistBarrierTransport{
		relistStarted: make(chan struct{}),
		releaseRelist: make(chan struct{}),
	}
}

func (t *relistBarrierTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Query().Get("watch") == "true" {
		response, err := t.trackingTransport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		disconnect := false
		t.disconnectOnce.Do(func() { disconnect = true })
		if disconnect {
			response.Body = &disconnectingBody{ReadCloser: response.Body}
		}
		return response, nil
	}

	if t.listRequests.Add(1) == 2 {
		t.relistOnce.Do(func() { close(t.relistStarted) })
		select {
		case <-t.releaseRelist:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return t.trackingTransport.RoundTrip(req)
}

func (t *relistBarrierTransport) setBase(base http.RoundTripper) {
	t.trackingTransport.setBase(base)
}

func (t *relistBarrierTransport) release() {
	t.releaseOnce.Do(func() { close(t.releaseRelist) })
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
	t.recordWatchQuery(req.URL.RawQuery)
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
	onDisconnect func()
	once         sync.Once
}

func (b *disconnectingBody) Read(_ []byte) (int, error) {
	b.once.Do(func() {
		if b.onDisconnect != nil {
			b.onDisconnect()
		}
		_ = b.Close()
	})
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
	require.NotEmpty(t, kubeconfig, "KUBECONFIG must point to the temporary kind kubeconfig")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	require.NoError(t, err, "build integration kubeconfig")
	config.ContentType = "application/json"
	config.AcceptContentTypes = "application/json"
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

func clientWithTransport(t *testing.T, config *rest.Config, transport roundTripperWrapper) *kubernetes.Clientset {
	t.Helper()
	wrapper := transportWrapper(config, transport)
	client, err := kubernetes.NewForConfig(wrapper)
	require.NoError(t, err)
	return client
}

func assertRolloutRequestOptions(t *testing.T, transport *trackingTransport, workloadName string) {
	t.Helper()
	listQueries, watchQueries := transport.queries()
	require.NotEmpty(t, listQueries)
	require.NotEmpty(t, watchQueries)
	expectedSelector := fields.OneTermEqualSelector("metadata.name", workloadName).String()
	for _, rawQuery := range listQueries {
		query, err := url.ParseQuery(rawQuery)
		require.NoError(t, err)
		assert.Equal(t, expectedSelector, query.Get("fieldSelector"))
		assert.Empty(t, query.Get("watch"))
	}
	for _, rawQuery := range watchQueries {
		query, err := url.ParseQuery(rawQuery)
		require.NoError(t, err)
		assert.Equal(t, expectedSelector, query.Get("fieldSelector"))
		assert.Equal(t, "true", query.Get("watch"))
		assert.Equal(t, "true", query.Get("allowWatchBookmarks"))
		assert.NotEmpty(t, query.Get("resourceVersion"))
	}
}

func assertRecoveryResourceVersions(t *testing.T, watchVersions, listVersions []string) {
	t.Helper()
	require.GreaterOrEqual(t, len(watchVersions), 2)
	require.GreaterOrEqual(t, len(listVersions), 2)
	for index := range 2 {
		require.NotEmpty(t, listVersions[index])
		assert.Equal(
			t,
			listVersions[index],
			watchVersions[index],
			"watch %d must start from its preceding fresh list resourceVersion",
			index+1,
		)
	}
}

func updateDeploymentTemplateAnnotation(
	t *testing.T,
	client kubernetes.Interface,
	namespace, name, value string,
) (*appsv1.Deployment, error) {
	t.Helper()
	var updated *appsv1.Deployment
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := client.AppsV1().Deployments(namespace).Get(
			t.Context(),
			name,
			metav1.GetOptions{},
		)
		if err != nil {
			return err
		}
		deployment.Spec.Template.Annotations = map[string]string{"rollout-watch": value}
		updated, err = client.AppsV1().Deployments(namespace).Update(
			t.Context(),
			deployment,
			metav1.UpdateOptions{},
		)
		return err
	})
	return updated, err
}

func updateStatefulSetTemplateAnnotation(
	t *testing.T,
	client kubernetes.Interface,
	namespace, name, value string,
) (*appsv1.StatefulSet, error) {
	t.Helper()
	var updated *appsv1.StatefulSet
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		statefulSet, err := client.AppsV1().StatefulSets(namespace).Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		statefulSet.Spec.Template.Annotations = map[string]string{"rollout-watch": value}
		updated, err = client.AppsV1().StatefulSets(namespace).Update(t.Context(), statefulSet, metav1.UpdateOptions{})
		return err
	})
	return updated, err
}

func updateDaemonSetTemplateAnnotation(
	t *testing.T,
	client kubernetes.Interface,
	namespace, name, value string,
) (*appsv1.DaemonSet, error) {
	t.Helper()
	var updated *appsv1.DaemonSet
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		daemonSet, err := client.AppsV1().DaemonSets(namespace).Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		daemonSet.Spec.Template.Annotations = map[string]string{"rollout-watch": value}
		updated, err = client.AppsV1().DaemonSets(namespace).Update(t.Context(), daemonSet, metav1.UpdateOptions{})
		return err
	})
	return updated, err
}

func updateDeploymentStatus(
	t *testing.T,
	client kubernetes.Interface,
	namespace, name string,
	mutate func(*appsv1.Deployment),
) (*appsv1.Deployment, error) {
	t.Helper()
	var updated *appsv1.Deployment
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := client.AppsV1().Deployments(namespace).Get(
			t.Context(),
			name,
			metav1.GetOptions{},
		)
		if err != nil {
			return err
		}
		mutate(deployment)
		updated, err = client.AppsV1().Deployments(namespace).UpdateStatus(
			t.Context(),
			deployment,
			metav1.UpdateOptions{},
		)
		return err
	})
	return updated, err
}
func resumeJob(
	t *testing.T,
	client kubernetes.Interface,
	namespace, name string,
) (*batchv1.Job, error) {
	t.Helper()
	var updated *batchv1.Job
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := client.BatchV1().Jobs(namespace).Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		current.Spec.Suspend = ptr.To(false)
		updated, err = client.BatchV1().Jobs(namespace).Update(t.Context(), current, metav1.UpdateOptions{})
		return err
	})
	return updated, err
}

func updateJobStatus(
	t *testing.T,
	client kubernetes.Interface,
	namespace, name string,
	mutate func(*batchv1.Job),
) (*batchv1.Job, error) {
	t.Helper()
	var updated *batchv1.Job
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		job, err := client.BatchV1().Jobs(namespace).Get(
			t.Context(),
			name,
			metav1.GetOptions{},
		)
		if err != nil {
			return err
		}
		mutate(job)
		updated, err = client.BatchV1().Jobs(namespace).UpdateStatus(
			t.Context(),
			job,
			metav1.UpdateOptions{},
		)
		return err
	})
	return updated, err
}

func updateStatefulSetStatus(
	t *testing.T,
	client kubernetes.Interface,
	namespace, name string,
	mutate func(*appsv1.StatefulSet),
) (*appsv1.StatefulSet, error) {
	t.Helper()
	var updated *appsv1.StatefulSet
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		statefulSet, err := client.AppsV1().StatefulSets(namespace).Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		mutate(statefulSet)
		updated, err = client.AppsV1().StatefulSets(namespace).UpdateStatus(t.Context(), statefulSet, metav1.UpdateOptions{})
		return err
	})
	return updated, err
}

func updateDaemonSetStatus(
	t *testing.T,
	client kubernetes.Interface,
	namespace, name string,
	mutate func(*appsv1.DaemonSet),
) (*appsv1.DaemonSet, error) {
	t.Helper()
	var updated *appsv1.DaemonSet
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		daemonSet, err := client.AppsV1().DaemonSets(namespace).Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		mutate(daemonSet)
		updated, err = client.AppsV1().DaemonSets(namespace).UpdateStatus(t.Context(), daemonSet, metav1.UpdateOptions{})
		return err
	})
	return updated, err
}

type observeOutcome struct {
	result observer.WatchResult
	err    error
}

type observeCall struct {
	outcome <-chan observeOutcome
	done    <-chan struct{}
	cancel  context.CancelFunc
}

func startObserve(
	parent context.Context,
	t *testing.T,
	rolloutObserver observer.RolloutObserver,
	ref observer.ResourceRef,
	expectedGeneration int64,
	timeout time.Duration,
) observeCall {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	outcomes := make(chan observeOutcome, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := rolloutObserver.Observe(ctx, ref, expectedGeneration, timeout)
		outcomes <- observeOutcome{result: result, err: err}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("Observe goroutine did not exit during cleanup")
		}
	})
	return observeCall{outcome: outcomes, done: done, cancel: cancel}
}

func awaitObserve(t *testing.T, call observeCall, timeout time.Duration) observeOutcome {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var outcome observeOutcome
	select {
	case outcome = <-call.outcome:
	case <-timer.C:
		call.cancel()
		select {
		case <-call.done:
		case <-time.After(5 * time.Second):
			t.Fatal("Observe goroutine did not exit after cancellation")
		}
		t.Fatalf("Observe did not return within %s", timeout)
	}

	select {
	case <-call.done:
	case <-time.After(time.Second):
		t.Fatal("Observe goroutine did not exit after returning a result")
	}
	return outcome
}

func assertRolloutLast(t *testing.T, result observer.WatchResult, err error) {
	t.Helper()
	var rolloutErr *observer.RolloutError
	require.ErrorAs(t, err, &rolloutErr)
	assert.Equal(t, result, rolloutErr.Last)
}

func workloadPodTemplate(name string, selector map[string]string, image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: selector},
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: ptr.To[int64](0),
			Containers: []corev1.Container{{
				Name:            name,
				Image:           image,
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
	tests := []struct {
		name   string
		create func(*rolloutFixture, *testing.T) (observer.ResourceRef, int64)
	}{
		{
			name: "deployment",
			create: func(fx *rolloutFixture, t *testing.T) (observer.ResourceRef, int64) {
				deployment := fx.createDeployment(t, "deployment")
				return fx.deploymentRef(deployment.Name), deployment.Generation
			},
		},
		{
			name: "statefulset",
			create: func(fx *rolloutFixture, t *testing.T) (observer.ResourceRef, int64) {
				statefulSet := fx.createStatefulSet(t, "statefulset")
				return fx.statefulSetRef(statefulSet.Name), statefulSet.Generation
			},
		},
		{
			name: "daemonset",
			create: func(fx *rolloutFixture, t *testing.T) (observer.ResourceRef, int64) {
				daemonSet := fx.createDaemonSet(t, "daemonset")
				return fx.daemonSetRef(daemonSet.Name), daemonSet.Generation
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := setupRolloutFixture(t, t.Name())
			ref, generation := test.create(fx, t)

			result, err := observer.New(fx.observerClient(t)).Observe(t.Context(), ref, generation, defaultSceneTimeout)

			require.NoError(t, err)
			assert.Equal(t, ref, result.Resource)
			assert.True(t, result.Ready)
			assert.False(t, result.Failed)
			assert.NotEmpty(t, result.ResourceUID)
			assert.NotEmpty(t, result.ResourceVersion)
			assertAppsReadyGeneration(t, result, generation)
			fx.assertClean(t)
		})
	}
}

func TestRolloutWatchJobCompletesAfterWatch(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	job := fx.createJob(t, "job")
	ref := fx.jobRef(job.Name)

	obs := observer.New(fx.observerClient(t))
	call := startObserve(t.Context(), t, obs, ref, 0, defaultSceneTimeout)

	require.Eventually(
		t,
		func() bool { return fx.transport.activeWatches.Load() == 1 },
		5*time.Second,
		10*time.Millisecond,
	)
	resumed, err := resumeJob(t, fx.adminClient, fx.namespace, job.Name)
	require.NoError(t, err)

	outcome := awaitObserve(t, call, defaultSceneTimeout)
	require.NoError(t, outcome.err)
	result := outcome.result
	assert.Equal(t, ref, result.Resource)
	assert.True(t, result.Ready)
	assert.False(t, result.Failed)
	assert.Equal(t, job.UID, result.ResourceUID)
	assert.Equal(t, resumed.Generation, result.Generation)
	assert.Zero(t, result.ObservedGeneration)
	assert.NotEmpty(t, result.ResourceVersion)
	require.Condition(t, func() bool {
		for _, condition := range result.Conditions {
			if condition.Type == string(batchv1.JobComplete) &&
				condition.Status == string(corev1.ConditionTrue) {
				return true
			}
		}
		return false
	}, "Job controller did not report Complete=True")

	fx.assertClean(t)
}

func assertAppsReadyGeneration(t *testing.T, result observer.WatchResult, expectedGeneration int64) {
	t.Helper()
	assert.GreaterOrEqual(t, result.Generation, expectedGeneration)
	assert.GreaterOrEqual(t, result.ObservedGeneration, expectedGeneration)
}

func TestRolloutWatchAppsObservedGenerationGate(t *testing.T) {
	type generationSnapshot struct {
		generation         int64
		observedGeneration int64
	}
	tests := []struct {
		name   string
		create func(*rolloutFixture, *testing.T) (observer.ResourceRef, int64)
		update func(*rolloutFixture, *testing.T) (generationSnapshot, error)
	}{
		{
			name: "deployment",
			create: func(fx *rolloutFixture, t *testing.T) (observer.ResourceRef, int64) {
				deployment := fx.createDeployment(t, "deployment-generation")
				return fx.deploymentRef(deployment.Name), deployment.Generation + 1
			},
			update: func(fx *rolloutFixture, t *testing.T) (generationSnapshot, error) {
				updated, err := updateDeploymentTemplateAnnotation(
					t, fx.adminClient, fx.namespace, "deployment-generation", "next-generation",
				)
				if err != nil {
					return generationSnapshot{}, err
				}
				return generationSnapshot{updated.Generation, updated.Status.ObservedGeneration}, nil
			},
		},
		{
			name: "statefulset",
			create: func(fx *rolloutFixture, t *testing.T) (observer.ResourceRef, int64) {
				statefulSet := fx.createStatefulSet(t, "statefulset-generation")
				return fx.statefulSetRef(statefulSet.Name), statefulSet.Generation + 1
			},
			update: func(fx *rolloutFixture, t *testing.T) (generationSnapshot, error) {
				updated, err := updateStatefulSetTemplateAnnotation(
					t, fx.adminClient, fx.namespace, "statefulset-generation", "next-generation",
				)
				if err != nil {
					return generationSnapshot{}, err
				}
				return generationSnapshot{updated.Generation, updated.Status.ObservedGeneration}, nil
			},
		},
		{
			name: "daemonset",
			create: func(fx *rolloutFixture, t *testing.T) (observer.ResourceRef, int64) {
				daemonSet := fx.createDaemonSet(t, "daemonset-generation")
				return fx.daemonSetRef(daemonSet.Name), daemonSet.Generation + 1
			},
			update: func(fx *rolloutFixture, t *testing.T) (generationSnapshot, error) {
				updated, err := updateDaemonSetTemplateAnnotation(
					t, fx.adminClient, fx.namespace, "daemonset-generation", "next-generation",
				)
				if err != nil {
					return generationSnapshot{}, err
				}
				return generationSnapshot{updated.Generation, updated.Status.ObservedGeneration}, nil
			},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := setupRolloutFixture(t, fmt.Sprintf("apps-generation-%02d", index))
			ref, expectedGeneration := test.create(fx, t)
			transport := newWatchEventBarrierTransport(`"rollout-watch":"next-generation"`)
			t.Cleanup(transport.release)
			call := startObserve(
				t.Context(),
				t,
				observer.New(clientWithTransport(t, fx.observerCfg, transport)),
				ref,
				expectedGeneration,
				defaultSceneTimeout,
			)
			require.Eventually(
				t,
				func() bool { return transport.activeWatches.Load() == 1 },
				5*time.Second,
				10*time.Millisecond,
			)

			updated, err := test.update(fx, t)
			require.NoError(t, err)
			assert.Less(t, updated.observedGeneration, updated.generation)
			select {
			case <-transport.matched:
			case <-time.After(5 * time.Second):
				t.Fatal("observer watch did not receive the generation update")
			}
			select {
			case <-transport.waiting:
			case <-time.After(5 * time.Second):
				t.Fatal("observer did not continue waiting after the stale observedGeneration event")
			}
			select {
			case outcome := <-call.outcome:
				t.Fatalf("observer returned before observedGeneration caught up: result=%+v err=%v", outcome.result, outcome.err)
			default:
			}

			transport.release()
			outcome := awaitObserve(t, call, defaultSceneTimeout)
			require.NoError(t, outcome.err)
			assert.True(t, outcome.result.Ready)
			assert.GreaterOrEqual(t, outcome.result.Generation, updated.generation)
			assert.GreaterOrEqual(t, outcome.result.ObservedGeneration, updated.generation)
			assertTransportClean(t, &transport.trackingTransport)
			fx.assertClean(t)
		})
	}
}
func TestRolloutWatchValidatesBeforeAPI(t *testing.T) {
	tests := []struct {
		name       string
		ref        func(*rolloutFixture) observer.ResourceRef
		generation int64
		timeout    time.Duration
		wantCode   observer.ErrorCode
	}{
		{
			name: "empty namespace",
			ref: func(*rolloutFixture) observer.ResourceRef {
				return observer.ResourceRef{GVR: observer.DeploymentGVR, Name: "deployment"}
			},
			generation: 1,
			timeout:    time.Second,
			wantCode:   observer.ErrorCodeInvalidArgument,
		},
		{
			name: "empty name",
			ref: func(fx *rolloutFixture) observer.ResourceRef {
				return observer.ResourceRef{GVR: observer.DeploymentGVR, Namespace: fx.namespace}
			},
			generation: 1,
			timeout:    time.Second,
			wantCode:   observer.ErrorCodeInvalidArgument,
		},
		{
			name: "invalid apps generation",
			ref: func(fx *rolloutFixture) observer.ResourceRef {
				return fx.deploymentRef("deployment")
			},
			generation: 0,
			timeout:    time.Second,
			wantCode:   observer.ErrorCodeInvalidArgument,
		},
		{
			name: "invalid job generation",
			ref: func(fx *rolloutFixture) observer.ResourceRef {
				return fx.jobRef("job")
			},
			generation: 1,
			timeout:    time.Second,
			wantCode:   observer.ErrorCodeInvalidArgument,
		},
		{
			name: "invalid timeout",
			ref: func(fx *rolloutFixture) observer.ResourceRef {
				return fx.deploymentRef("deployment")
			},
			generation: 1,
			timeout:    0,
			wantCode:   observer.ErrorCodeInvalidArgument,
		},
		{
			name: "unsupported resource",
			ref: func(fx *rolloutFixture) observer.ResourceRef {
				return observer.ResourceRef{
					GVR: schema.GroupVersionResource{
						Group:    "example.io",
						Version:  "v1",
						Resource: "widgets",
					},
					Namespace: fx.namespace,
					Name:      "widget",
				}
			},
			generation: 0,
			timeout:    time.Second,
			wantCode:   observer.ErrorCodeUnsupportedResource,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := setupRolloutFixture(t, fmt.Sprintf("validate-%02d-%s", index, test.name))
			ref := test.ref(fx)
			beforeLists := fx.transport.listCalls.Load()
			beforeWatches := fx.transport.watchCalls.Load()

			result, err := observer.New(fx.observerClient(t)).Observe(t.Context(), ref, test.generation, test.timeout)

			require.Error(t, err)
			assert.Equal(t, test.wantCode, observerCode(t, err))
			assert.Equal(t, ref, result.Resource)
			assertRolloutLast(t, result, err)
			assert.Equal(t, beforeLists, fx.transport.listCalls.Load())
			assert.Equal(t, beforeWatches, fx.transport.watchCalls.Load())
			fx.assertClean(t)
		})
	}
}

func TestRolloutWatchWorkloadFailures(t *testing.T) {
	t.Run("deployment terminal failures", func(t *testing.T) {
		tests := []struct {
			name      string
			condition appsv1.DeploymentCondition
		}{
			{
				name: "replica failure",
				condition: appsv1.DeploymentCondition{
					Type:    appsv1.DeploymentReplicaFailure,
					Status:  corev1.ConditionTrue,
					Reason:  "FailedCreate",
					Message: "replica creation failed",
				},
			},
			{
				name: "progress deadline exceeded",
				condition: appsv1.DeploymentCondition{
					Type:    appsv1.DeploymentProgressing,
					Status:  corev1.ConditionFalse,
					Reason:  "ProgressDeadlineExceeded",
					Message: "deployment exceeded its progress deadline",
				},
			},
		}

		for index, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				fx := setupRolloutFixture(t, fmt.Sprintf("deployment-failure-%02d", index))
				deployment := fx.createDeployment(t, "deployment-failed")
				obs := observer.New(fx.observerClient(t))
				ref := fx.deploymentRef(deployment.Name)
				call := startObserve(t.Context(), t, obs,
					ref,
					deployment.Generation+1,
					defaultSceneTimeout)
				require.Eventually(
					t,
					func() bool { return fx.transport.activeWatches.Load() > 0 },
					5*time.Second,
					10*time.Millisecond,
				)
				updated, err := updateDeploymentStatus(
					t,
					fx.adminClient,
					fx.namespace,
					deployment.Name,
					func(current *appsv1.Deployment) {
						current.Status.Conditions = []appsv1.DeploymentCondition{test.condition}
					},
				)
				require.NoError(t, err)
				outcome := awaitObserve(t, call, defaultSceneTimeout)
				result := outcome.result
				err = outcome.err
				assert.ErrorIs(t, err, observer.ErrWorkloadUnavailable)
				assert.Equal(t, observer.ErrorCodeWorkloadUnavailable, observerCode(t, err))
				assert.Equal(t, ref, result.Resource)
				assert.Equal(t, deployment.UID, result.ResourceUID)
				assert.Equal(t, updated.Generation, result.Generation)
				assert.Equal(t, updated.Status.ObservedGeneration, result.ObservedGeneration)
				assert.Equal(t, updated.ResourceVersion, result.ResourceVersion)
				assert.False(t, result.Ready)
				assert.True(t, result.Failed)
				require.Len(t, result.Conditions, 1)
				assert.Equal(t, string(test.condition.Type), result.Conditions[0].Type)
				assert.Equal(t, string(test.condition.Status), result.Conditions[0].Status)
				assert.Equal(t, test.condition.Reason, result.Conditions[0].Reason)
				assert.Equal(t, test.condition.Message, result.Conditions[0].Message)
				assertRolloutLast(t, result, err)
				fx.assertClean(t)
			})
		}
	})

	t.Run("job terminal failure", func(t *testing.T) {
		fx := setupRolloutFixture(t, t.Name())
		job := fx.createPendingJob(t, "job-failed")
		obs := observer.New(fx.observerClient(t))
		ref := fx.jobRef(job.Name)
		call := startObserve(t.Context(), t, obs, ref, 0, defaultSceneTimeout)
		require.Eventually(
			t,
			func() bool { return fx.transport.activeWatches.Load() > 0 },
			5*time.Second,
			10*time.Millisecond,
		)
		updated, err := updateJobStatus(
			t,
			fx.adminClient,
			fx.namespace,
			job.Name,
			func(current *batchv1.Job) {
				current.Status.Active = 0
				current.Status.Conditions = []batchv1.JobCondition{
					{
						Type:    batchv1.JobFailureTarget,
						Status:  corev1.ConditionTrue,
						Reason:  "BackoffLimitExceeded",
						Message: "job reached the backoff limit",
					},
					{
						Type:    batchv1.JobFailed,
						Status:  corev1.ConditionTrue,
						Reason:  "BackoffLimitExceeded",
						Message: "job reached the backoff limit",
					},
				}
				if current.Status.StartTime == nil {
					current.Status.StartTime = &metav1.Time{Time: time.Now()}
				}
			},
		)
		require.NoError(t, err)
		outcome := awaitObserve(t, call, defaultSceneTimeout)
		result := outcome.result
		err = outcome.err
		assert.ErrorIs(t, err, observer.ErrWorkloadUnavailable)
		assert.Equal(t, observer.ErrorCodeWorkloadUnavailable, observerCode(t, err))
		assert.Equal(t, ref, result.Resource)
		assert.Equal(t, job.UID, result.ResourceUID)
		assert.Equal(t, updated.Generation, result.Generation)
		assert.Zero(t, result.ObservedGeneration)
		assert.Equal(t, updated.ResourceVersion, result.ResourceVersion)
		assert.False(t, result.Ready)
		assert.True(t, result.Failed)
		require.Len(t, result.Conditions, 2)
		failed := result.Conditions[1]
		assert.Equal(t, string(batchv1.JobFailed), failed.Type)
		assert.Equal(t, string(corev1.ConditionTrue), failed.Status)
		assert.Equal(t, "BackoffLimitExceeded", failed.Reason)
		assert.Equal(t, "job reached the backoff limit", failed.Message)
		assertRolloutLast(t, result, err)
		fx.assertClean(t)
	})

	t.Run("statefulset non-terminal condition does not fail", func(t *testing.T) {
		fx := setupRolloutFixture(t, t.Name())
		statefulSet := fx.createStatefulSet(t, "statefulset-pending")
		transport := newWatchEventBarrierTransport(`"reason":"RolloutPending"`)
		t.Cleanup(transport.release)
		obs := observer.New(clientWithTransport(t, fx.observerCfg, transport))
		ref := fx.statefulSetRef(statefulSet.Name)
		call := startObserve(t.Context(), t, obs,
			ref,
			statefulSet.Generation+100,
			3*time.Second)
		require.Eventually(t, func() bool { return transport.activeWatches.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
		updated, err := updateStatefulSetStatus(
			t,
			fx.adminClient,
			fx.namespace,
			statefulSet.Name,
			func(current *appsv1.StatefulSet) {
				current.Status.Conditions = []appsv1.StatefulSetCondition{{
					Type:   "Progressing",
					Status: corev1.ConditionFalse,
					Reason: "RolloutPending",
				}}
			},
		)
		require.NoError(t, err)
		select {
		case <-transport.matched:
		case <-time.After(2 * time.Second):
			t.Fatal("observer watch did not receive the StatefulSet non-terminal condition")
		}
		select {
		case <-transport.waiting:
		case <-time.After(2 * time.Second):
			t.Fatal("observer did not continue waiting after the StatefulSet non-terminal condition")
		}
		outcome := awaitObserve(t, call, 5*time.Second)
		result := outcome.result
		err = outcome.err
		assert.ErrorIs(t, err, observer.ErrRolloutTimeout)
		assert.False(t, errors.Is(err, observer.ErrWorkloadUnavailable))
		assertRolloutLast(t, result, err)
		require.Len(t, result.Conditions, 1)
		assert.Equal(t, "Progressing", result.Conditions[0].Type)
		assert.Equal(t, string(corev1.ConditionFalse), result.Conditions[0].Status)
		assert.Equal(t, "RolloutPending", result.Conditions[0].Reason)
		assert.Equal(t, updated.ResourceVersion, result.ResourceVersion)
		assertTransportClean(t, &transport.trackingTransport)
		fx.assertClean(t)
	})

	t.Run("daemonset non-terminal condition does not fail", func(t *testing.T) {
		fx := setupRolloutFixture(t, t.Name())
		daemonSet := fx.createDaemonSet(t, "daemonset-pending")
		transport := newWatchEventBarrierTransport(`"reason":"RolloutPending"`)
		t.Cleanup(transport.release)
		obs := observer.New(clientWithTransport(t, fx.observerCfg, transport))
		ref := fx.daemonSetRef(daemonSet.Name)
		call := startObserve(t.Context(), t, obs,
			ref,
			daemonSet.Generation+100,
			3*time.Second)
		require.Eventually(t, func() bool { return transport.activeWatches.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
		updated, err := updateDaemonSetStatus(
			t,
			fx.adminClient,
			fx.namespace,
			daemonSet.Name,
			func(current *appsv1.DaemonSet) {
				current.Status.Conditions = []appsv1.DaemonSetCondition{{
					Type:   "Progressing",
					Status: corev1.ConditionFalse,
					Reason: "RolloutPending",
				}}
			},
		)
		require.NoError(t, err)
		select {
		case <-transport.matched:
		case <-time.After(2 * time.Second):
			t.Fatal("observer watch did not receive the DaemonSet non-terminal condition")
		}
		select {
		case <-transport.waiting:
		case <-time.After(2 * time.Second):
			t.Fatal("observer did not continue waiting after the DaemonSet non-terminal condition")
		}
		outcome := awaitObserve(t, call, 5*time.Second)
		result := outcome.result
		err = outcome.err
		assert.ErrorIs(t, err, observer.ErrRolloutTimeout)
		assert.False(t, errors.Is(err, observer.ErrWorkloadUnavailable))
		assertRolloutLast(t, result, err)
		require.Len(t, result.Conditions, 1)
		assert.Equal(t, "Progressing", result.Conditions[0].Type)
		assert.Equal(t, string(corev1.ConditionFalse), result.Conditions[0].Status)
		assert.Equal(t, "RolloutPending", result.Conditions[0].Reason)
		assert.Equal(t, updated.ResourceVersion, result.ResourceVersion)
		assertTransportClean(t, &transport.trackingTransport)
		fx.assertClean(t)
	})
}

func TestRolloutWatchTransportDisconnect(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	deployment := fx.createDeployment(t, "reconnect")
	ref := fx.deploymentRef(deployment.Name)

	transport := &disconnectingTransport{
		isFirstWatchDisconnectEnabled: true,
		disconnected:                  make(chan struct{}),
		secondListReady:               make(chan struct{}),
		releaseSecondList:             make(chan struct{}),
	}
	t.Cleanup(transport.releaseRelist)
	client := clientWithTransport(t, fx.observerCfg, transport)
	obs := observer.New(client)

	call := startObserve(t.Context(), t, obs,
		ref,
		deployment.Generation+1,
		defaultSceneTimeout)

	select {
	case <-transport.disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("observer did not consume the injected watch EOF")
	}
	select {
	case <-transport.secondListReady:
	case <-time.After(5 * time.Second):
		t.Fatal("observer did not complete the fresh list after watch EOF")
	}
	updated, err := updateDeploymentTemplateAnnotation(t, fx.adminClient, fx.namespace, deployment.Name, "ready")
	require.NoError(t, err)
	transport.releaseRelist()

	outcome := awaitObserve(t, call, defaultSceneTimeout)
	result := outcome.result
	require.NoError(t, outcome.err)
	assert.True(t, result.Ready)
	assert.GreaterOrEqual(t, result.ObservedGeneration, updated.Generation)
	assert.NotEmpty(t, result.ResourceVersion)
	assert.GreaterOrEqual(t, transport.listCalls.Load(), int64(2))
	assert.GreaterOrEqual(t, transport.watchCalls.Load(), int64(2))
	assertRecoveryResourceVersions(t, transport.resourceVersions(), transport.listResourceVersions())
	assertRolloutRequestOptions(t, &transport.trackingTransport, deployment.Name)

	assertTransportClean(t, &transport.trackingTransport)
	fx.assertClean(t)
}

func TestRolloutWatchResourceVersionExpired(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	deployment := fx.createDeployment(t, "expired")
	ref := fx.deploymentRef(deployment.Name)

	transport := &expiredTransport{}
	client := clientWithTransport(t, fx.observerCfg, transport)
	obs := observer.New(client)

	call := startObserve(t.Context(), t, obs,
		ref,
		deployment.Generation+1,
		defaultSceneTimeout)

	require.Eventually(t, func() bool { return transport.watchCalls.Load() >= 2 }, 5*time.Second, 10*time.Millisecond)
	updated, err := updateDeploymentTemplateAnnotation(
		t,
		fx.adminClient,
		fx.namespace,
		deployment.Name,
		"expired-recovery",
	)
	require.NoError(t, err)

	outcome := awaitObserve(t, call, defaultSceneTimeout)
	result := outcome.result
	require.NoError(t, outcome.err)
	assert.True(t, result.Ready)
	assert.NotEmpty(t, result.ResourceVersion)
	assert.GreaterOrEqual(t, transport.listCalls.Load(), int64(2))
	assert.GreaterOrEqual(t, result.ObservedGeneration, updated.Generation)
	assertRecoveryResourceVersions(t, transport.resourceVersions(), transport.listResourceVersions())
	assertRolloutRequestOptions(t, &transport.trackingTransport, deployment.Name)

	assertTransportClean(t, &transport.trackingTransport)
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
	assert.Equal(t, deployment.UID, result.ResourceUID)
	assert.Equal(t, deployment.Generation, result.Generation)
	assert.NotEmpty(t, result.ResourceVersion)
	fx.assertClean(t)
}

func TestRolloutWatchParentCancelWinsTimeout(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	deployment := fx.createDeployment(t, "cancel")
	const boundaryTimeout = 2 * time.Second

	for i := range 3 {
		t.Run(fmt.Sprintf("trial-%d", i), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), boundaryTimeout)
			defer cancel()
			transport := &trackingTransport{}
			obsCfg := &rest.Config{}
			*obsCfg = *fx.observerCfg
			obsCfg.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
				transport.base = base
				return transport
			}
			client, err := kubernetes.NewForConfig(obsCfg)
			require.NoError(t, err)

			call := startObserve(ctx, t, observer.New(client),
				fx.deploymentRef(deployment.Name),
				deployment.Generation+100,
				boundaryTimeout)

			require.Eventually(t, func() bool { return transport.activeWatches.Load() == 1 }, time.Second, 10*time.Millisecond)

			outcome := awaitObserve(t, call, boundaryTimeout+5*time.Second)
			result := outcome.result
			err = outcome.err
			assert.ErrorIs(t, err, observer.ErrCancelled)
			assert.Equal(t, observer.ErrorCodeCancelled, observerCode(t, err))
			assertRolloutLast(t, result, err)
			assert.Equal(t, deployment.UID, result.ResourceUID)
			assert.Equal(t, deployment.Generation, result.Generation)
			assert.NotEmpty(t, result.ResourceVersion)
			assert.Eventually(t, func() bool { return transport.activeWatches.Load() == 0 }, 5*time.Second, 100*time.Millisecond)
		})
	}

	fx.assertClean(t)
}

func TestRolloutWatchDeleteAndReplace(t *testing.T) {
	t.Run("deleted object returns workload_unavailable", func(t *testing.T) {
		fx := setupRolloutFixture(t, t.Name())
		deployment := fx.createDeployment(t, "delete")
		ref := fx.deploymentRef(deployment.Name)
		obs := observer.New(fx.observerClient(t))
		call := startObserve(t.Context(), t, obs,
			ref,
			deployment.Generation+1,
			defaultSceneTimeout)
		require.Eventually(
			t,
			func() bool { return fx.transport.activeWatches.Load() > 0 },
			5*time.Second,
			10*time.Millisecond,
		)
		err := fx.adminClient.AppsV1().Deployments(fx.namespace).Delete(
			t.Context(),
			deployment.Name,
			metav1.DeleteOptions{},
		)
		require.NoError(t, err)
		outcome := awaitObserve(t, call, defaultSceneTimeout)
		result := outcome.result
		err = outcome.err
		assert.ErrorIs(t, err, observer.ErrWorkloadUnavailable)
		assert.Equal(t, deployment.UID, result.ResourceUID)
		assertRolloutLast(t, result, err)
		fx.assertClean(t)
	})

	t.Run("replacement UID is rejected after fresh list", func(t *testing.T) {
		fx := setupRolloutFixture(t, t.Name())
		deployment := fx.createDeployment(t, "replace")
		ref := fx.deploymentRef(deployment.Name)
		transport := newRelistBarrierTransport()
		t.Cleanup(transport.release)
		client := clientWithTransport(t, fx.observerCfg, transport)
		obs := observer.New(client)
		call := startObserve(t.Context(), t, obs,
			ref,
			deployment.Generation+1,
			defaultSceneTimeout)

		select {
		case <-transport.relistStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("observer did not start a fresh list after watch disconnect")
		}
		require.Eventually(t, func() bool { return transport.activeWatches.Load() == 0 }, 5*time.Second, 10*time.Millisecond)

		propagation := metav1.DeletePropagationForeground
		err := fx.adminClient.AppsV1().Deployments(fx.namespace).Delete(
			t.Context(),
			deployment.Name,
			metav1.DeleteOptions{PropagationPolicy: &propagation},
		)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			_, err := fx.adminClient.AppsV1().Deployments(fx.namespace).Get(t.Context(), deployment.Name, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 10*time.Second, 100*time.Millisecond)
		replacement := fx.createDeployment(t, deployment.Name)
		transport.release()

		outcome := awaitObserve(t, call, defaultSceneTimeout)
		result := outcome.result
		err = outcome.err
		assert.ErrorIs(t, err, observer.ErrWorkloadUnavailable)
		assert.Equal(t, deployment.UID, result.ResourceUID)
		assert.NotEqual(t, replacement.UID, result.ResourceUID)
		assertRolloutLast(t, result, err)
		assert.GreaterOrEqual(t, transport.listCalls.Load(), int64(2))
		assert.Equal(t, int64(1), transport.watchCalls.Load())
		assertTransportClean(t, &transport.trackingTransport)
		fx.assertClean(t)
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
	firstListCalls := fx.transport.listCalls.Load()

	second, err := obs.Observe(t.Context(), ref, deployment.Generation, defaultSceneTimeout)
	require.NoError(t, err)
	assert.True(t, second.Ready)
	assert.Equal(t, first.ResourceUID, second.ResourceUID)
	assert.Equal(t, first.Generation, second.Generation)
	assert.Greater(t, fx.transport.listCalls.Load(), firstListCalls)

	fx.assertClean(t)
}

func TestRolloutWatchConcurrentIsolation(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	deployment := fx.createDeployment(t, "concurrent")
	ref := fx.deploymentRef(deployment.Name)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	obs := observer.New(fx.observerClient(t))
	firstCall := startObserve(ctx, t, obs,
		ref,
		deployment.Generation+100,
		defaultSceneTimeout)
	secondCall := startObserve(t.Context(), t, obs,
		ref,
		deployment.Generation+1,
		defaultSceneTimeout)

	require.Eventually(
		t,
		func() bool { return fx.transport.activeWatches.Load() == 2 },
		5*time.Second,
		10*time.Millisecond,
	)
	updated, err := updateDeploymentTemplateAnnotation(
		t,
		fx.adminClient,
		fx.namespace,
		deployment.Name,
		"concurrent-ready",
	)
	require.NoError(t, err)

	cancel()

	firstOutcome := awaitObserve(t, firstCall, defaultSceneTimeout)
	secondOutcome := awaitObserve(t, secondCall, defaultSceneTimeout)
	assert.ErrorIs(t, firstOutcome.err, observer.ErrCancelled)
	assert.False(t, firstOutcome.result.Ready)
	require.NoError(t, secondOutcome.err)
	assert.True(t, secondOutcome.result.Ready)
	assert.GreaterOrEqual(t, secondOutcome.result.ObservedGeneration, updated.Generation)

	fx.assertClean(t)
}

func TestRolloutWatchRBACSecretProbe(t *testing.T) {
	fx := setupRolloutFixture(t, t.Name())
	deployment := fx.createDeployment(t, "rbac")
	observerClient := fx.observerClient(t)
	obs := observer.New(observerClient)

	result, err := obs.Observe(t.Context(), fx.deploymentRef(deployment.Name), deployment.Generation, defaultSceneTimeout)
	require.NoError(t, err)
	assert.True(t, result.Ready)

	probeConfig := rest.CopyConfig(fx.observerCfg)
	probeConfig.WrapTransport = nil
	probeClient, err := kubernetes.NewForConfig(probeConfig)
	require.NoError(t, err)
	assertSelfAccess(t, probeClient, authorizationv1.ResourceAttributes{
		Namespace: fx.namespace,
		Verb:      "list",
		Group:     "apps",
		Resource:  "deployments",
	}, true)
	assertSelfAccess(t, probeClient, authorizationv1.ResourceAttributes{
		Namespace: fx.namespace,
		Verb:      "list",
		Resource:  "secrets",
	}, false)
	assertSelfAccess(t, probeClient, authorizationv1.ResourceAttributes{
		Namespace: fx.namespace,
		Verb:      "list",
		Resource:  "configmaps",
	}, false)
	assertSelfAccess(t, probeClient, authorizationv1.ResourceAttributes{
		Namespace: fx.namespace,
		Verb:      "create",
		Group:     "apps",
		Resource:  "deployments",
	}, false)
	fx.assertNoSecrets(t)
	fx.assertClean(t)
}

func assertSelfAccess(
	t *testing.T,
	client kubernetes.Interface,
	attributes authorizationv1.ResourceAttributes,
	wantAllowed bool,
) {
	t.Helper()
	review, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(
		t.Context(),
		&authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attributes},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, wantAllowed, review.Status.Allowed, review.Status.Reason)
}

func observerCode(t *testing.T, err error) observer.ErrorCode {
	t.Helper()
	var rolloutErr *observer.RolloutError
	require.ErrorAs(t, err, &rolloutErr)
	return rolloutErr.Code()
}
