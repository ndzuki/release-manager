// Package preflight implements Cluster DryRun preflight for the operator agent.
// It performs server-side dry-run against the target Kubernetes API server using
// client-go Dynamic Client with DryRunAll, classifying responses into stable
// error codes without mutating cluster state.
package preflight

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Input carries the full context for a dry-run preflight.
// It is received from the orchestrator via an outbox command payload.
type Input struct {
	// OperationID identifies the release operation for observability.
	OperationID string `json:"operation_id"`
	// RenderDigest is the digest of the rendered manifest from REQ-046.
	RenderDigest string `json:"render_digest"`
	// CapabilityVersion is the cluster capability snapshot version.
	CapabilityVersion string `json:"capability_version"`
	// ManifestStream is the rendered YAML/JSON manifest stream (multi-document).
	ManifestStream []byte `json:"manifest_stream"`
	// TargetNamespace is the default namespace for namespaced resources.
	TargetNamespace string `json:"target_namespace"`
	// TargetServiceAccount is informational only — no impersonation.
	TargetServiceAccount string `json:"target_service_account,omitempty"`
}

// ResourceResult captures the outcome of a single resource dry-run.
// MUST NOT expose raw object body, Secret data, or stringData.
type ResourceResult struct {
	// GVK is the GroupVersionKind of the resource.
	GVK schema.GroupVersionKind `json:"gvk"`
	// Name is the object name (from metadata.name), empty if parse failed.
	Name string `json:"name"`
	// Namespace is the target namespace (empty for cluster-scoped resources).
	Namespace string `json:"namespace,omitempty"`
	// Accepted is true when the dry-run request returned without error.
	Accepted bool `json:"accepted"`
	// Rejected is true when the dry-run was rejected by the API server.
	Rejected bool `json:"rejected"`
	// ErrorCode is the stable classification code (kubernetes_forbidden, etc.).
	ErrorCode string `json:"error_code,omitempty"`
	// Reason is a human-readable sanitized reason extracted from the API error.
	Reason string `json:"reason,omitempty"`
	// Duration is the time spent on the dry-run request.
	Duration time.Duration `json:"duration"`
}

// BatchResult summarizes all resource-level results in a dry-run batch.
type BatchResult struct {
	// OperationID matches the input operation.
	OperationID string `json:"operation_id"`
	// RenderDigest matches the input render digest.
	RenderDigest string `json:"render_digest"`
	// CapabilityVersion matches the input capability version.
	CapabilityVersion string `json:"capability_version"`
	// Passed is true when every resource was Accepted.
	Passed bool `json:"passed"`
	// ResourceCount is the number of resources evaluated.
	ResourceCount int `json:"resource_count"`
	// Results contains individual resource outcomes.
	// When Passed is false, the first rejected resource is deterministically ordered.
	Results []ResourceResult `json:"results"`
	// Duration is the total wall-clock time for the batch.
	Duration time.Duration `json:"duration"`
}

// DryRunOption configures how a dry-run request is performed.
type DryRunOption int

const (
	// DryRunCreate sends a server-side dry-run Create for each resource.
	DryRunCreate DryRunOption = iota
	// DryRunUpdate sends a server-side dry-run Update; caller must provide resourceVersion.
	DryRunUpdate
)

// Classification of API server responses into stable error codes.
const (
	ErrKubernetesForbidden   = "kubernetes_forbidden"
	ErrAdmissionRejected     = "admission_rejected"
	ErrQuotaExceeded         = "quota_exceeded"
	ErrAPINotSupported       = "api_not_supported"
	ErrNamespaceMissing      = "namespace_missing"
	ErrDryRunUnavailable     = "dryrun_unavailable"
	ErrUnknown               = "preflight_unknown"
)

// Sentinel errors for the preflight package.
var (
	ErrEmptyManifest       = errSentinel("manifest stream is empty")
	ErrOverSizedManifest   = errSentinel("manifest stream exceeds size limit")
	ErrTooManyResources    = errSentinel("too many resources in manifest")
	ErrPreflightCancelled  = errSentinel("preflight cancelled")
)

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// knownClassified maps classification codes that are user-visible.
var knownClassified = map[string]struct{}{
	ErrKubernetesForbidden: {},
	ErrAdmissionRejected:   {},
	ErrQuotaExceeded:       {},
	ErrAPINotSupported:     {},
	ErrNamespaceMissing:    {},
	ErrDryRunUnavailable:   {},
}

// IsKnownErrorCode returns true when code is a stable preflight error code.
func IsKnownErrorCode(code string) bool {
	_, ok := knownClassified[code]
	return ok
}

// FailureFirst returns the first rejected resource from a batch, or nil.
func (b *BatchResult) FailureFirst() *ResourceResult {
	for i := range b.Results {
		if b.Results[i].Rejected {
			return &b.Results[i]
		}
	}
	return nil
}

// AllowedGVKList is a union of well-known GVKs that the operator's SA is
// expected to interact with during installs.
var AllowedGVKList = []schema.GroupVersionKind{
	{Group: "", Version: "v1", Kind: "ConfigMap"},
	{Group: "", Version: "v1", Kind: "Secret"},
	{Group: "", Version: "v1", Kind: "Service"},
	{Group: "", Version: "v1", Kind: "ServiceAccount"},
	{Group: "", Version: "v1", Kind: "PersistentVolumeClaim"},
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "apps", Version: "v1", Kind: "StatefulSet"},
	{Group: "apps", Version: "v1", Kind: "DaemonSet"},
	{Group: "batch", Version: "v1", Kind: "Job"},
	{Group: "batch", Version: "v1", Kind: "CronJob"},
	{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"},
	{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"},
	{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"},
	{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"},
	{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"},
}

// ParseTimeouts for individual resource and total batch.
const (
	DefaultResourceTimeout  = 10 * time.Second
	DefaultBatchTimeout     = 5 * time.Minute
	MaxManifestBytes        = 50 * 1024 * 1024 // 50 MiB
	MaxResourceDocs         = 500
)

// well-known legacy group alias: extensions/v1beta1 etc. — always treat as api_not_supported.
var unsupportedGroupVersions = map[string]bool{
	"extensions/v1beta1": true,
	"apps/v1beta1":       true,
	"apps/v1beta2":       true,
	"batch/v1beta1":      true,
	"batch/v2alpha1":     true,
	"policy/v1beta1":     true,
	"networking/v1beta1": true,
}

// IsUnsupportedGroupVersion returns true when the discovery result maps a GVK
// into a known-deprecated/removed group+version on this cluster.
func IsUnsupportedGroupVersion(gvr schema.GroupVersionResource) bool {
	return unsupportedGroupVersions[gvr.GroupVersion().String()]
}


// DryRunAll constant used in API requests.
const dryRunAll = metav1.DryRunAll
