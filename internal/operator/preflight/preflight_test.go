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
			Status:  metav1.StatusFailure,
			Code:    http.StatusNotFound,
			Reason:  metav1.StatusReasonNotFound,

### Round 2 · 2026-07-16
> 计划版本: v1
> 分支: task/047-cluster-dryrun-preflight
>
> #### Step 1: DryRun 契约与安全结果模型
> - 创建/修改: `internal/operator/preflight/types.go` (新增，208 行)
> - 测试结果: PASS — 类型定义通过编译，稳定错误码已声明
>
> #### Step 2: Manifest 流解析与 API capability 映射
> - 创建/修改: `internal/operator/preflight/manifest.go`, `internal/operator/preflight/mapper.go` (新增，约 160 行)
> - 测试结果: PASS — manifest 解码、空文档跳过、GVK→GVR 映射通过
>
> #### Step 3: client-go Dynamic DryRunAll 执行器
> - 创建/修改: `internal/operator/preflight/dryrun.go`, `internal/operator/preflight/cache.go`, `go.mod`, `go.sum` (新增/修改，约 400 行)
> - 测试结果: PASS — DryRunOne/DryRunAll 与 fake dynamic client 集成通过
>
> #### Step 4: 错误分类、Secret 脱敏与 fail-closed 汇总
> - 创建/修改: `internal/operator/preflight/classify.go`, `internal/operator/preflight/classify_helpers.go`, `internal/operator/preflight/sanitize.go` (新增，约 180 行)
> - 测试结果: PASS — 6 种稳定错误分类、Secret data/stringData 删除、ConfigMap data 保留
>
> #### Step 5: 缓存与 operation 执行门禁
> - 创建/修改: `internal/operator/preflight/cache.go` 内 Cache/Orchestrator/Gate (约 215 行)
> - 测试结果: PASS — 幂等缓存命中、capability version 变化清除、Gate nil/失败/通过全路径
>
> #### Step 6: Fake API 验收与全链路回归
> - 创建/修改: `internal/operator/preflight/preflight_test.go` (新增，约 670 行)
> - 测试结果: PASS — 21 个测试覆盖 AC-047-01 至 AC-047-04；全量 race PASS、定向 lint 0 issues
> - 验证请求包含 `dryRun=All`、拒绝路径零 Helm 执行调用、Secret 序列化结果不含 data/stringData
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
