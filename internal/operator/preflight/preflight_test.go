package preflight

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8smeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

// testScheme builds a minimal runtime.Scheme for the fake dynamic client.
func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "", Version: "v1", Kind: "Secret"},
		{Group: "", Version: "v1", Kind: "ConfigMap"},
		{Group: "", Version: "v1", Kind: "Service"},
		{Group: "", Version: "v1", Kind: "Pod"},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"},
	} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	return s
}

// newDefaultMapper builds a DefaultRESTMapper for well-known GVKs.
func newDefaultMapper(gvs []schema.GroupVersion) k8smeta.ResettableRESTMapper {
	mapper := k8smeta.NewDefaultRESTMapper(gvs)
	mapper.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"}, k8smeta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, k8smeta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}, k8smeta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}, k8smeta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, k8smeta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}, k8smeta.RESTScopeRoot)
	return staticMapper{mapper}
}

// testMapper builds a GKVMapper backed by a fake dynamic client.
func testMapper(t *testing.T) *GKVMapper {
	t.Helper()

	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)

	gvs := []schema.GroupVersion{
		{Group: "", Version: "v1"},
		{Group: "apps", Version: "v1"},
		{Group: "rbac.authorization.k8s.io", Version: "v1"},
	}
	rm := newDefaultMapper(gvs)

	return GKVMapperWithFake(dynClient, rm)
}

// ── Manifest Parsing Tests ──

func TestDecodeManifestStream_Valid(t *testing.T) {
	yaml := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
  namespace: default
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deploy
  namespace: default
`
	resources, err := DecodeManifestStream([]byte(yaml))
	require.NoError(t, err)
	assert.Len(t, resources, 2)
	assert.Equal(t, "ConfigMap", resources[0].GetKind())
	assert.Equal(t, "Deployment", resources[1].GetKind())
}

func TestDecodeManifestStream_Empty(t *testing.T) {
	_, err := DecodeManifestStream([]byte{})
	assert.ErrorIs(t, err, ErrEmptyManifest)
}

func TestDecodeManifestStream_EmptyDocument(t *testing.T) {
	yaml := `
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: real
`
	resources, err := DecodeManifestStream([]byte(yaml))
	require.NoError(t, err)
	assert.Len(t, resources, 1)
	assert.Equal(t, "ConfigMap", resources[0].GetKind())
}

// ── Error Classification Tests ──

func TestErrorCodeFromAPIError_Forbidden(t *testing.T) {
	err := apierrors.NewForbidden(
		schema.GroupResource{Resource: "deployments", Group: "apps"},
		"my-deploy",
		assert.AnError,
	)
	code := ErrorCodeFromAPIError(err)
	assert.Equal(t, ErrKubernetesForbidden, code)
}

func TestErrorCodeFromAPIError_NamespaceNotFound(t *testing.T) {
	err := &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Status: metav1.StatusFailure,
			Code:   http.StatusNotFound,
			Reason: metav1.StatusReasonNotFound,

			Message: `namespaces "missing-ns" not found`,
		},
	}
	code := ErrorCodeFromAPIError(err)
	assert.Equal(t, ErrNamespaceMissing, code)
}

func TestErrorCodeFromAPIError_AdmissionRejected(t *testing.T) {
	err := &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Status:  metav1.StatusFailure,
			Code:    http.StatusForbidden,
			Reason:  metav1.StatusReasonForbidden,
			Message: `admission webhook "validating.example.com" denied the request`,
		},
	}
	code := ErrorCodeFromAPIError(err)
	assert.Equal(t, ErrAdmissionRejected, code)
}

func TestErrorCodeFromAPIError_APINotSupported(t *testing.T) {
	err := &k8smeta.NoResourceMatchError{
		PartialResource: schema.GroupVersionResource{
			Group:    "unsupported.example.com",
			Version:  "v1",
			Resource: "things",
		},
	}
	code := ErrorCodeFromAPIError(err)
	assert.Equal(t, ErrAPINotSupported, code)
}

func TestErrorCodeFromAPIError_QuotaExceeded(t *testing.T) {
	err := &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Status:  metav1.StatusFailure,
			Code:    http.StatusForbidden,
			Reason:  metav1.StatusReasonForbidden,
			Message: `exceeded quota: project-quota`,
		},
	}
	code := ErrorCodeFromAPIError(err)
	assert.Equal(t, ErrQuotaExceeded, code)
}

func TestErrorCodeFromAPIError_DryRunUnavailable(t *testing.T) {
	err := &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Status:  metav1.StatusFailure,
			Code:    http.StatusBadRequest,
			Reason:  metav1.StatusReasonInvalid,
			Message: `Invalid: dryRun feature is not enabled`,
		},
	}
	code := ErrorCodeFromAPIError(err)
	assert.Equal(t, ErrDryRunUnavailable, code)
}

// ── Secret Sanitization Tests (AC-047-04) ──

func TestSanitizeResource_SecretRemovesData(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name": "my-secret",
			},
			"data": map[string]interface{}{
				"key": "c2VjcmV0Cg==",
			},
			"stringData": map[string]interface{}{
				"plain": "hello",
			},
		},
	}

	sanitized := sanitizeResource(obj)

	content := sanitized.UnstructuredContent()
	_, hasData := content["data"]
	_, hasStringData := content["stringData"]

	assert.False(t, hasData, "data field should be removed")
	assert.False(t, hasStringData, "stringData field should be removed")
	assert.Equal(t, "my-secret", sanitized.GetName())
}

func TestSanitizeResource_NonSecretPreserved(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name": "my-config",
			},
			"data": map[string]interface{}{
				"key": "value",
			},
		},
	}

	sanitized := sanitizeResource(obj)

	content := sanitized.UnstructuredContent()
	assert.Contains(t, content, "data", "ConfigMap data should be preserved")
}

// ── DRY-RUN EXECUTION TESTS ──

// makeUnstructured creates an unstructured object from GVK, name, namespace.
//
//nolint:unparam // test helper always uses "default" namespace
func makeUnstructured(gvk schema.GroupVersionKind, name, namespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetName(name)
	u.SetNamespace(namespace)
	return u
}

// makeSecret creates a Secret unstructured object.
func makeSecret(name, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"data": map[string]interface{}{
				"token": "c2VjcmV0",
			},
		},
	}
	return obj
}

func TestDryRunOne_Success(t *testing.T) {
	m := testMapper(t)
	exec := NewDryRunExecutor(m)

	obj := makeUnstructured(
		schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"},
		"test-cm", "default",
	)

	rr := exec.DryRunOne(context.Background(), obj, DryRunCreate)

	assert.True(t, rr.Accepted, "expected accepted")
	assert.False(t, rr.Rejected)
	assert.Equal(t, "ConfigMap", rr.GVK.Kind)
	assert.NotZero(t, rr.Duration)
}

func TestDryRunOne_ForbiddenAC04702(t *testing.T) {
	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)

	// Prepend a reactor that returns Forbidden for all creates.
	dynClient.PrependReactor("create", "*", func(_ ktesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "configmaps"},
			"test-cm",
			assert.AnError,
		)
	})

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	obj := makeUnstructured(
		schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"},
		"test-cm", "default",
	)

	rr := exec.DryRunOne(context.Background(), obj, DryRunCreate)

	assert.True(t, rr.Rejected, "expected rejected")
	assert.False(t, rr.Accepted)
	assert.Equal(t, ErrKubernetesForbidden, rr.ErrorCode)
}

func TestDryRunOne_APINotSupportedAC04703(t *testing.T) {
	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	// A GVK not registered in the mapper.
	obj := makeUnstructured(
		schema.GroupVersionKind{Group: "unsupported.example.com", Version: "v1", Kind: "Thing"},
		"my-thing", "default",
	)

	rr := exec.DryRunOne(context.Background(), obj, DryRunCreate)

	assert.True(t, rr.Rejected, "expected rejected")
	assert.Equal(t, ErrAPINotSupported, rr.ErrorCode)
}

func TestDryRunOne_AdmissionRejectedAC04701(t *testing.T) {
	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)

	dynClient.PrependReactor("create", "*", func(_ ktesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, &apierrors.StatusError{
			ErrStatus: metav1.Status{
				Status:  metav1.StatusFailure,
				Code:    http.StatusForbidden,
				Reason:  metav1.StatusReasonForbidden,
				Message: `admission webhook "validator.example.com" denied the request: spec.replicas must be >=1`,
			},
		}
	})

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	obj := makeUnstructured(
		schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"},
		"test-cm", "default",
	)

	rr := exec.DryRunOne(context.Background(), obj, DryRunCreate)

	assert.True(t, rr.Rejected, "expected rejected")
	assert.Equal(t, ErrAdmissionRejected, rr.ErrorCode)
}

func TestDryRunOne_SingleAPICallForbidden(t *testing.T) {
	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)

	callCount := 0
	dynClient.PrependReactor("create", "*", func(_ ktesting.Action) (handled bool, ret runtime.Object, err error) {
		callCount++
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "configmaps"},
			"test-cm",
			assert.AnError,
		)
	})

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	obj := makeUnstructured(
		schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"},
		"test-cm", "default",
	)

	rr := exec.DryRunOne(context.Background(), obj, DryRunCreate)

	assert.Equal(t, 1, callCount, "forbidden should be a single attempt, no escalation")
	assert.True(t, rr.Rejected)
}

func TestDryRunOne_DryRunOptionSet(t *testing.T) {
	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)

	var capturedOpts metav1.CreateOptions
	dynClient.PrependReactor("create", "*", func(action ktesting.Action) (handled bool, ret runtime.Object, err error) {
		if ca, ok := action.(ktesting.CreateActionImpl); ok {
			capturedOpts = ca.CreateOptions
		}
		return false, nil, nil
	})

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	obj := makeUnstructured(
		schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"},
		"test-cm", "default",
	)

	_ = exec.DryRunOne(context.Background(), obj, DryRunCreate)

	assert.Contains(t, capturedOpts.DryRun, metav1.DryRunAll)
	assert.Equal(t, "release-manager-preflight", capturedOpts.FieldManager)
}

func TestDryRunOne_SecretNoDataInResult(t *testing.T) {
	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	obj := makeSecret("my-secret", "default")

	rr := exec.DryRunOne(context.Background(), obj, DryRunCreate)

	// The result itself should never contain raw object data.
	assert.True(t, rr.Accepted)
	reason := rr.Reason
	assert.NotContains(t, reason, "c2VjcmV0", "Secret base64 should not appear in result")
}

// ── Dry-run semantics for an object that already exists ──
//
// These two tests pin the API semantics the cluster stage depends on, before
// any implementation relies on them: a server-side dry-run CREATE for a name
// that already exists is rejected (AlreadyExists), while a server-side dry-run
// UPDATE of the same object is accepted. An UPGRADE dry-runs the objects a
// previous INSTALL already created, so the Create semantics can never pass for
// it — that was the e2e failure this pair exists to prevent from regressing.

func TestDryRunOne_CreateRejectsExistingObject(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	dynClient := fake.NewSimpleDynamicClient(testScheme(), makeUnstructured(gvk, "dep-1", "default"))

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "apps", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	rr := exec.DryRunOne(context.Background(), makeUnstructured(gvk, "dep-1", "default"), DryRunCreate)

	require.True(t, rr.Rejected, "a dry-run Create for an existing name must be rejected")
	assert.Contains(t, rr.Reason, "already exists", "the rejection reason must name the conflict")
	assert.False(t, rr.Accepted)
}

func TestDryRunOne_UpdateAcceptsExistingObject(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	dynClient := fake.NewSimpleDynamicClient(testScheme(), makeUnstructured(gvk, "dep-1", "default"))

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "apps", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	rr := exec.DryRunOne(context.Background(), makeUnstructured(gvk, "dep-1", "default"), DryRunUpdate)

	require.False(t, rr.Rejected, "a dry-run Update of an existing object must be accepted: %s", rr.Reason)
	assert.True(t, rr.Accepted)
}

// TestDryRunAll_ExistingObjectsUseUpdateSemantics is the regression gate for the
// e2e UPGRADE failure: the objects a previous install created already exist, so
// an always-Create batch rejects the first one (AlreadyExists) and the cluster
// stage fails forever after the first release. The same batch must pass when the
// executor picks the Update semantics for existing objects.
//
// Mutation check: forcing DryRunCreate in DryRunAll makes this test fail.
func TestDryRunAll_ExistingObjectsUseUpdateSemantics(t *testing.T) {
	s := testScheme()
	existing := []runtime.Object{
		makeUnstructured(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, "cm-1", "default"),
		makeUnstructured(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, "dep-1", "default"),
	}
	dynClient := fake.NewSimpleDynamicClient(s, existing...)

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
		{Group: "apps", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	// The rendered objects are the same ones that are already deployed.
	resources := []*unstructured.Unstructured{
		makeUnstructured(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, "cm-1", "default"),
		makeUnstructured(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, "dep-1", "default"),
	}

	result, err := exec.DryRunAll(context.Background(), resources, Input{OperationID: "op-upgrade", TargetNamespace: "default"})
	require.NoError(t, err)

	require.True(t, result.Passed, "an already-deployed release must pass its dry-run: %+v", result.Results)
	assert.Equal(t, 2, result.ResourceCount)
	assert.True(t, Gate(result))

	// Both objects were validated as Updates, never as Creates.
	verbs := recordedWriteVerbs(dynClient)
	assert.Equal(t, []string{"update", "update"}, verbs)
}

// TestDryRunAll_FreshObjectsUseCreateSemantics keeps the first-install path
// honest: an object that does not exist yet is dry-run Created (an Update of a
// missing object would be rejected with NotFound).
func TestDryRunAll_FreshObjectsUseCreateSemantics(t *testing.T) {
	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
		{Group: "apps", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	resources := []*unstructured.Unstructured{
		makeUnstructured(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, "cm-1", "default"),
		makeUnstructured(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, "dep-1", "default"),
	}

	result, err := exec.DryRunAll(context.Background(), resources, Input{OperationID: "op-install", TargetNamespace: "default"})
	require.NoError(t, err)

	require.True(t, result.Passed, "a first install must pass its dry-run: %+v", result.Results)
	assert.Equal(t, []string{"create", "create"}, recordedWriteVerbs(dynClient))
}

// TestDryRunAll_EveryWriteIsAServerSideDryRun is the guard against "make it pass
// by not really validating": whichever semantics is chosen, the request that
// reaches the API server must carry DryRun=All. A client-side validation or a
// skipped object would leave the write actions with an empty DryRun list.
func TestDryRunAll_EveryWriteIsAServerSideDryRun(t *testing.T) {
	s := testScheme()
	// cm-1 exists (Update path), dep-1 does not (Create path): both branches run
	// in one batch.
	dynClient := fake.NewSimpleDynamicClient(s,
		makeUnstructured(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, "cm-1", "default"))

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
		{Group: "apps", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	resources := []*unstructured.Unstructured{
		makeUnstructured(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, "cm-1", "default"),
		makeUnstructured(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, "dep-1", "default"),
	}
	result, err := exec.DryRunAll(context.Background(), resources, Input{OperationID: "op-mixed"})
	require.NoError(t, err)
	require.True(t, result.Passed)

	var verbs []string
	for _, action := range dynClient.Actions() {
		switch action.GetVerb() {
		case "create", "update":
			verbs = append(verbs, action.GetVerb())
			assert.Contains(t, dryRunOptionOf(action), metav1.DryRunAll,
				"the %s must be a server-side dry-run", action.GetVerb())
		}
	}
	assert.ElementsMatch(t, []string{"create", "update"}, verbs,
		"one branch of each semantics must run")
}

// dryRunOptionOf extracts the DryRun option list a fake-client write action was
// built with. The options live on the concrete action implementations.
func dryRunOptionOf(action ktesting.Action) []string {
	switch a := action.(type) {
	case ktesting.CreateActionImpl:
		return a.CreateOptions.DryRun
	case *ktesting.CreateActionImpl:
		return a.CreateOptions.DryRun
	case ktesting.UpdateActionImpl:
		return a.UpdateOptions.DryRun
	case *ktesting.UpdateActionImpl:
		return a.UpdateOptions.DryRun
	}
	return nil
}

// TestDryRunAll_ProbeFailureFailsClosed: only a genuine NotFound may fall back to
// Create. A probe that fails for any other reason (here: Forbidden) cannot prove
// the object's lifecycle, so the stage must fail closed instead of guessing.
func TestDryRunAll_ProbeFailureFailsClosed(t *testing.T) {
	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)
	dynClient.PrependReactor("get", "configmaps", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "configmaps"}, "cm-1", assert.AnError)
	})

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
	}))
	exec := NewDryRunExecutor(m)

	result, err := exec.DryRunAll(context.Background(), []*unstructured.Unstructured{
		makeUnstructured(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, "cm-1", "default"),
	}, Input{OperationID: "op-probe-fail"})
	require.NoError(t, err)

	require.False(t, result.Passed, "an unprovable lifecycle must not be certified")
	assert.Equal(t, ErrKubernetesForbidden, result.Results[0].ErrorCode)
}

// recordedWriteVerbs returns the verbs of the write actions the fake client saw,
// in order.
func recordedWriteVerbs(dynClient *fake.FakeDynamicClient) []string {
	verbs := make([]string, 0, len(dynClient.Actions()))
	for _, action := range dynClient.Actions() {
		switch action.GetVerb() {
		case "create", "update":
			verbs = append(verbs, action.GetVerb())
		}
	}
	return verbs
}

// ── Batch / Gate Tests ──

func TestDryRunAll_AllAccepted(t *testing.T) {
	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
		{Group: "apps", Version: "v1"},
	}))

	exec := NewDryRunExecutor(m)

	resources := []*unstructured.Unstructured{
		makeUnstructured(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, "cm-1", "default"),
		makeUnstructured(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, "dep-1", "default"),
	}

	input := Input{OperationID: "op-1", TargetNamespace: "default"}

	result, err := exec.DryRunAll(context.Background(), resources, input)
	require.NoError(t, err)

	assert.True(t, result.Passed)
	assert.Equal(t, 2, result.ResourceCount)
	assert.True(t, Gate(result), "gate should pass when all accepted")
}

func TestDryRunAll_FirstRejectedStopsBatch(t *testing.T) {
	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)

	dynClient.PrependReactor("create", "configmaps", func(_ ktesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "configmaps"},
			"cm-1",
			assert.AnError,
		)
	})

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
		{Group: "apps", Version: "v1"},
	}))

	exec := NewDryRunExecutor(m)

	resources := []*unstructured.Unstructured{
		makeUnstructured(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, "cm-1", "default"),
		makeUnstructured(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, "dep-1", "default"),
	}

	result, err := exec.DryRunAll(context.Background(), resources, Input{OperationID: "op-2"})
	require.NoError(t, err)

	assert.False(t, result.Passed)
	assert.Equal(t, 1, result.ResourceCount, "should stop after first rejection")
	assert.False(t, Gate(result), "gate should fail")
}

func TestGate_NilResult(t *testing.T) {
	assert.False(t, Gate(nil))
}

// ── Cache Tests ──

func TestCache_HitAndMiss(t *testing.T) {
	c := NewCache()

	r1 := &BatchResult{
		OperationID:       "op-1",
		RenderDigest:      "sha256:abc123",
		CapabilityVersion: "v1",
		Passed:            true,
	}

	// Miss before put.
	_, ok := c.Get("sha256:abc123", "v1")
	assert.False(t, ok)

	// Put then hit.
	c.Put(r1, "v1")
	got, ok := c.Get("sha256:abc123", "v1")
	assert.True(t, ok)
	assert.Equal(t, "op-1", got.OperationID)
}

func TestCache_Invalidate(t *testing.T) {
	c := NewCache()
	c.Put(&BatchResult{RenderDigest: "d1", CapabilityVersion: "v1"}, "v1")
	c.Invalidate()
	_, ok := c.Get("d1", "v1")
	assert.False(t, ok)
}

// ── Cluster-Scope Tests ──

func TestIsClusterScoped(t *testing.T) {
	tests := []struct {
		kind     string
		expected bool
	}{
		{"ClusterRole", true},
		{"ClusterRoleBinding", true},
		{"Namespace", true},
		{"PersistentVolume", true},
		{"Deployment", false},
		{"Service", false},
		{"ConfigMap", false},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsClusterScoped(tt.kind))
		})
	}
}

// ── SanitizeResultJSON Tests ──

func TestSanitizeResultJSON_Secret(t *testing.T) {
	json := `{"data":{"key":"val"},"stringData":{"k":"v"},"metadata":{"name":"s"}}`
	cleaned := sanitizeResultJSON(json, "Secret")
	assert.NotContains(t, cleaned, `"data":`)
	assert.NotContains(t, cleaned, `"stringData":`)
}

func TestSanitizeResultJSON_NonSecret(t *testing.T) {
	json := `{"data":{"key":"val"}}`
	cleaned := sanitizeResultJSON(json, "ConfigMap")
	assert.Contains(t, cleaned, `"data":`)
}

// ── ResultJSON Encoding Tests ──

func TestResultJSON_RoundTrip(t *testing.T) {
	result := &BatchResult{
		OperationID:       "op-1",
		RenderDigest:      "d1",
		CapabilityVersion: "v1",
		Passed:            true,
		ResourceCount:     2,
		Results: []ResourceResult{
			{
				GVK:      schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"},
				Name:     "cm-1",
				Accepted: true,
				Duration: 5 * time.Millisecond,
			},
		},
		Duration: 10 * time.Millisecond,
	}

	jsonStr, err := ResultJSON(result)
	require.NoError(t, err)
	assert.Contains(t, jsonStr, `"passed":true`)
	assert.Contains(t, jsonStr, `"resource_count":2`)
}

func TestDigest(t *testing.T) {
	d1 := Digest([]byte("manifest-data"))
	d2 := Digest([]byte("manifest-data"))
	d3 := Digest([]byte("different-data"))

	assert.Equal(t, d1, d2, "same input → same digest")
	assert.NotEqual(t, d1, d3, "different input → different digest")
	assert.NotEmpty(t, d1)
}

// ── IsKnownErrorCode Tests ──

func TestIsKnownErrorCode(t *testing.T) {
	assert.True(t, IsKnownErrorCode(ErrKubernetesForbidden))
	assert.True(t, IsKnownErrorCode(ErrAdmissionRejected))
	assert.True(t, IsKnownErrorCode(ErrQuotaExceeded))
	assert.True(t, IsKnownErrorCode(ErrAPINotSupported))
	assert.True(t, IsKnownErrorCode(ErrNamespaceMissing))
	assert.True(t, IsKnownErrorCode(ErrDryRunUnavailable))
	assert.False(t, IsKnownErrorCode("random_code"))
}

func TestPreflightOrchestratorCacheIntegration(t *testing.T) {
	s := testScheme()
	dynClient := fake.NewSimpleDynamicClient(s)

	m := GKVMapperWithFake(dynClient, newDefaultMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
	}))

	cache := NewCache()
	orch := NewOrchestratorWithCache(m, cache)

	manifestYAML := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
  namespace: default
`
	input := Input{
		OperationID:       "op-cache-test",
		RenderDigest:      Digest([]byte(manifestYAML)),
		CapabilityVersion: "v1",
		ManifestStream:    []byte(manifestYAML),
		TargetNamespace:   "default",
	}

	result, err := orch.Run(context.Background(), input)
	require.NoError(t, err)
	assert.True(t, result.Passed)

	// Second run should hit cache (no API call needed).
	result2, err := orch.Run(context.Background(), input)
	require.NoError(t, err)
	assert.True(t, result2.Passed)

	// Verify cache hit (same result).
	assert.Equal(t, result.OperationID, result2.OperationID)
}

func TestDecodeManifestStream_OverSized(t *testing.T) {
	huge := make([]byte, MaxManifestBytes+1)
	_, err := DecodeManifestStream(huge)
	assert.ErrorIs(t, err, ErrOverSizedManifest)
}

// ADR-026: an empty capability version cannot prove freshness, so the cache must
// be a MISS for it. It used to skip the staleness check when its tracked version
// was empty and serve whatever matched, which replayed an unversioned "passed"
// after the cluster's capabilities had changed -- a fail-open. Removing the
// empty-version guard from Get makes this test fail.
func TestCache_EmptyCapabilityVersionIsAlwaysAMiss(t *testing.T) {
	c := NewCache()

	// Even a Put with the same empty version must not become a hit.
	c.Put(&BatchResult{RenderDigest: "d-empty", CapabilityVersion: "", Passed: true}, "")
	_, ok := c.Get("d-empty", "")
	assert.False(t, ok, "an unversioned result must never be served")

	// A real version still caches and still invalidates on change.
	c.Put(&BatchResult{RenderDigest: "d-real", CapabilityVersion: "v1", Passed: true}, "v1")
	_, ok = c.Get("d-real", "v1")
	assert.True(t, ok, "a versioned result must still hit")

	_, ok = c.Get("d-real", "v2")
	assert.False(t, ok, "a changed capability version must miss")
}
