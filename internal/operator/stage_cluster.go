package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/preflight"
)

// ClusterDryRunner sends the rendered objects through server-side dry-run.
// *preflight.DryRunExecutor implements it.
type ClusterDryRunner interface {
	DryRunAll(ctx context.Context, resources []*unstructured.Unstructured, input preflight.Input) (*preflight.BatchResult, error)
}

// NamespaceEnsurer makes the target namespace exist before the cluster stage
// dry-runs objects into it.
//
// The install path creates the target namespace as part of the install (Helm's
// CreateNamespace, which the orchestrator sets for INSTALL), so a first INSTALL
// into a namespace that does not exist yet must still pass preflight. A
// server-side dry-run cannot run against a missing namespace — the API server
// answers NotFound, which classifies as namespace_missing (REQ-047) — so the
// stage creates it first, exactly as the install will. A command that does not
// create its namespace is left alone: a missing namespace stays namespace_missing.
type NamespaceEnsurer interface {
	EnsureNamespace(ctx context.Context, name string) error
}

// KubeNamespaceEnsurer creates the target namespace with client-go.
type KubeNamespaceEnsurer struct {
	client kubernetes.Interface
}

// NewKubeNamespaceEnsurer builds a namespace ensurer over a Kubernetes client.
func NewKubeNamespaceEnsurer(client kubernetes.Interface) *KubeNamespaceEnsurer {
	return &KubeNamespaceEnsurer{client: client}
}

// EnsureNamespace creates the namespace, treating AlreadyExists as success so
// the call is idempotent across preflight retries.
func (e *KubeNamespaceEnsurer) EnsureNamespace(ctx context.Context, name string) error {
	if e == nil || e.client == nil {
		return fmt.Errorf("namespace ensurer requires a Kubernetes client")
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("namespace name is required")
	}
	_, err := e.client.CoreV1().Namespaces().Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}},
		metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure namespace %q: %w", name, err)
	}
	return nil
}

var _ NamespaceEnsurer = (*KubeNamespaceEnsurer)(nil)

// ClusterStageExecutor runs the preflight cluster stage: it renders the approved
// chart and asks the cluster itself whether the objects would be accepted.
//
// It renders for itself rather than reusing the render stage's output, because a
// stage is an independent command and must be self-sufficient; and it renders
// through RenderManifests rather than RenderPreflight because the objects must
// never reach RenderResult, which carries only safe summaries (AC-046-02).
type ClusterStageExecutor struct {
	charts       ChartLocator
	dryRunner    ClusterDryRunner
	namespaces   NamespaceEnsurer
	capabilities CapabilityVersioner
	plainHTTP    bool
	logger       *slog.Logger
}

// WithCapabilityVersioner attaches the ADR-026 V1 capability probe. Without it
// the stage reports an empty capability version, which makes the dry-run cache a
// miss (fail-closed) rather than reusing a result it cannot invalidate.
func (e *ClusterStageExecutor) WithCapabilityVersioner(v CapabilityVersioner) *ClusterStageExecutor {
	e.capabilities = v
	return e
}

// NewClusterStageExecutor builds the cluster stage executor. namespaces may be
// nil only for commands that do not create their target namespace.
func NewClusterStageExecutor(
	charts ChartLocator,
	dryRunner ClusterDryRunner,
	namespaces NamespaceEnsurer,
	plainHTTP bool,
	logger *slog.Logger,
) *ClusterStageExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &ClusterStageExecutor{
		charts: charts, dryRunner: dryRunner, namespaces: namespaces,
		plainHTTP: plainHTTP, logger: logger,
	}
}

// ExecuteStage renders the command's chart and dry-runs every rendered object.
func (e *ClusterStageExecutor) ExecuteStage(ctx context.Context, command *operatorv1.Command) (string, error) {
	if e == nil || e.charts == nil {
		return "", fmt.Errorf("cluster stage requires a chart locator")
	}
	if e.dryRunner == nil {
		return "", fmt.Errorf("cluster stage requires a dry-run runner")
	}
	if command == nil {
		return "", fmt.Errorf("cluster stage requires a command")
	}
	bundle := command.GetBundle()
	if bundle == nil {
		return "", fmt.Errorf("cluster stage requires a bundle")
	}
	chartRef := strings.TrimSpace(bundle.GetChartRef())
	if chartRef == "" {
		return "", fmt.Errorf("cluster stage requires a chart reference")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	stream, objects, result, err := e.renderForDryRun(ctx, command, bundle, chartRef)
	if err != nil {
		return "", err
	}

	// Mirror the install's CreateNamespace before dry-running: the rendered
	// objects are pinned to the target namespace, and a server-side dry-run
	// against a namespace that does not exist yet is rejected as
	// namespace_missing. Without this a first INSTALL could never pass its own
	// preflight (real CI run 2026-09-21).
	if command.GetCreateNamespace() {
		if e.namespaces == nil {
			return "", fmt.Errorf("cluster stage requires a namespace ensurer for a command that creates its namespace")
		}
		if err := e.namespaces.EnsureNamespace(ctx, command.GetNamespace()); err != nil {
			return "", fmt.Errorf("cluster stage: %w", err)
		}
	}

	capabilityVersion := e.capabilityVersion(ctx, command.GetOperationId())
	batch, err := e.dryRunner.DryRunAll(ctx, objects, preflight.Input{
		OperationID:       command.GetOperationId(),
		RenderDigest:      result.RenderDigest,
		ManifestStream:    stream,
		CapabilityVersion: capabilityVersion,
		TargetNamespace:   command.GetNamespace(),
		// ADR-026 V1: the discovery probe supplies the version the dry-run cache
		// invalidates against. A probe failure degrades to an empty version,
		// which the cache treats as a MISS -- safe, and it does not block the
		// release on a discovery hiccup.
	})
	if err != nil {
		return "", fmt.Errorf("cluster stage: %w", err)
	}
	if !batch.Passed {
		return "", fmt.Errorf("cluster stage: server-side dry-run rejected %d of %d objects: %s",
			rejectedCount(batch), batch.ResourceCount, describeRejected(batch))
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		return "", fmt.Errorf("cluster stage: encode result: %w", err)
	}
	e.logger.Info("preflight cluster stage passed",
		"operation_id", command.GetOperationId(), "objects", len(objects), "render_digest", result.RenderDigest)
	return string(encoded), nil
}

// renderForDryRun renders the command's chart and decodes the objects the
// cluster must accept. It renders for itself because a stage is an independent
// command, and through RenderManifests because the objects must never reach
// RenderResult (AC-046-02).
func (e *ClusterStageExecutor) renderForDryRun(
	ctx context.Context,
	command *operatorv1.Command,
	bundle *commonv1.ReleaseBundle,
	chartRef string,
) ([]byte, []*unstructured.Unstructured, *helmengine.RenderResult, error) {
	loaded, err := e.charts.LocateChart(chartRef, bundle.GetChartVersion(), e.plainHTTP)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cluster stage: %w", err)
	}
	values := command.GetValues()
	rendered, result, err := helmengine.RenderManifests(ctx, helmengine.RenderOptions{
		ReleaseName:  command.GetReleaseName(),
		Namespace:    command.GetNamespace(),
		Chart:        loaded,
		ChartDigest:  bundle.GetChartDigest(),
		Values:       values,
		ValuesDigest: digestOf(values),
		ValuesPatch:  command.GetValuesPatch(),
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cluster stage: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	stream := manifestStream(rendered)
	objects, err := preflight.DecodeManifestStream(stream)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cluster stage: decode rendered manifest: %w", err)
	}
	if len(objects) == 0 {
		return nil, nil, nil, fmt.Errorf("cluster stage: the rendered chart produced no objects to dry-run")
	}
	return stream, objects, result, nil
}

// manifestStream joins the rendered files into one multi-document stream, in a
// stable order so the same chart renders the same stream twice.
func manifestStream(rendered map[string]string) []byte {
	filenames := make([]string, 0, len(rendered))
	for filename := range rendered {
		if strings.HasSuffix(filename, "NOTES.txt") {
			continue
		}
		filenames = append(filenames, filename)
	}
	sort.Strings(filenames)
	var builder strings.Builder
	for _, filename := range filenames {
		content := rendered[filename]
		if strings.TrimSpace(content) == "" {
			continue
		}
		builder.WriteString("---\n")
		builder.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			builder.WriteString("\n")
		}
	}
	return []byte(builder.String())
}

var _ StageExecutor = (*ClusterStageExecutor)(nil)

// rejectedCount counts the objects the cluster refused, for the stage error.
func rejectedCount(batch *preflight.BatchResult) int {
	rejected := 0
	for index := range batch.Results {
		if batch.Results[index].Rejected {
			rejected++
		}
	}
	return rejected
}

// describeRejected renders the first rejected resource into a bounded diagnostic
// for the stage error. A bare count ("rejected 1 of 1") left the field unable to
// tell WHICH object the cluster refused and WHY — the cause had to be recovered
// from a fresh run with extra logging.
//
// The text is built only from ResourceResult, which by contract carries the GVK,
// name, namespace, stable error code and the API server's already-sanitized
// reason: no object body, no Secret data, no manifest. sanitizeResourceResult has
// already truncated the reason before the batch is handed back here.
func describeRejected(batch *preflight.BatchResult) string {
	rejected := batch.FailureFirst()
	if rejected == nil {
		return "no rejected resource recorded"
	}
	detail := resourceIdentifier(rejected.GVK.Kind, rejected.GVK.GroupVersion().String())
	if rejected.Name != "" {
		detail += " " + rejected.Name
	}
	if rejected.Namespace != "" {
		detail += " (namespace " + rejected.Namespace + ")"
	}
	if rejected.ErrorCode != "" {
		detail += " [" + rejected.ErrorCode + "]"
	}
	if rejected.Reason != "" {
		detail += ": " + rejected.Reason
	}
	return detail
}

// resourceIdentifier renders "apiVersion/Kind", falling back to the bare kind
// and finally to a placeholder, so a result that lost its type information still
// produces a readable diagnostic instead of an empty string.
func resourceIdentifier(kind, apiVersion string) string {
	switch {
	case apiVersion != "" && kind != "":
		return apiVersion + "/" + kind
	case kind != "":
		return kind
	case apiVersion != "":
		return apiVersion
	default:
		return "unknown resource"
	}
}

// capabilityVersion probes the cluster capability version the dry-run cache
// invalidates against (ADR-026 V1). A probe failure degrades to an empty version
// -- the cache treats that as a MISS, which is the safe direction -- rather than
// failing the stage on a discovery hiccup.
func (e *ClusterStageExecutor) capabilityVersion(ctx context.Context, operationID string) string {
	if e.capabilities == nil {
		return ""
	}
	probed, err := e.capabilities.CapabilityVersion(ctx)
	if err != nil {
		e.logger.Warn("capability version probe failed; dry-run cache disabled for this run",
			"operation_id", operationID, "err", err)
		return ""
	}
	return probed
}
