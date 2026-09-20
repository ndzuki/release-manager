package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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

// ClusterStageExecutor runs the preflight cluster stage: it renders the approved
// chart and asks the cluster itself whether the objects would be accepted.
//
// It renders for itself rather than reusing the render stage's output, because a
// stage is an independent command and must be self-sufficient; and it renders
// through RenderManifests rather than RenderPreflight because the objects must
// never reach RenderResult, which carries only safe summaries (AC-046-02).
type ClusterStageExecutor struct {
	charts    ChartLocator
	dryRunner ClusterDryRunner
	plainHTTP bool
	logger    *slog.Logger
}

// NewClusterStageExecutor builds the cluster stage executor.
func NewClusterStageExecutor(charts ChartLocator, dryRunner ClusterDryRunner, plainHTTP bool, logger *slog.Logger) *ClusterStageExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &ClusterStageExecutor{charts: charts, dryRunner: dryRunner, plainHTTP: plainHTTP, logger: logger}
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

	batch, err := e.dryRunner.DryRunAll(ctx, objects, preflight.Input{
		OperationID:     command.GetOperationId(),
		RenderDigest:    result.RenderDigest,
		ManifestStream:  stream,
		TargetNamespace: command.GetNamespace(),
		// CapabilityVersion is deliberately empty: this executor has no cluster
		// capability snapshot, and the dry-run cache treats a version it cannot
		// match as a miss, so an empty one can never produce a false hit.
	})
	if err != nil {
		return "", fmt.Errorf("cluster stage: %w", err)
	}
	if !batch.Passed {
		return "", fmt.Errorf("cluster stage: server-side dry-run rejected %d of %d objects",
			rejectedCount(batch), batch.ResourceCount)
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
