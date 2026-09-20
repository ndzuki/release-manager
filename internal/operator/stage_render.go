package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"helm.sh/helm/v3/pkg/chart"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
)

// ChartLocator resolves and loads a chart reference without a cluster.
type ChartLocator interface {
	LocateChart(chartRef, chartVersion string, plainHTTP bool) (*chart.Chart, error)
}

// RenderStageExecutor runs the preflight render stage: it renders the approved
// chart and values and reports the safe render result.
//
// Rendering is a check, not a release write: this executor never installs,
// upgrades, or touches a cluster (TASK-114 AC 2).
type RenderStageExecutor struct {
	charts    ChartLocator
	plainHTTP bool
	logger    *slog.Logger
}

// NewRenderStageExecutor builds the render stage executor. plainHTTP mirrors the
// engine's registry setting so a dev fixture registry over plain HTTP can be
// located the same way an install locates it.
func NewRenderStageExecutor(charts ChartLocator, plainHTTP bool, logger *slog.Logger) *RenderStageExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &RenderStageExecutor{charts: charts, plainHTTP: plainHTTP, logger: logger}
}

// ExecuteStage renders the command's chart and values and returns the render
// result as JSON.
func (e *RenderStageExecutor) ExecuteStage(ctx context.Context, command *operatorv1.Command) (string, error) {
	if e == nil || e.charts == nil {
		return "", fmt.Errorf("render stage requires a chart locator")
	}
	if command == nil {
		return "", fmt.Errorf("render stage requires a command")
	}
	bundle := command.GetBundle()
	if bundle == nil {
		return "", fmt.Errorf("render stage requires a bundle")
	}
	chartRef := strings.TrimSpace(bundle.GetChartRef())
	if chartRef == "" {
		return "", fmt.Errorf("render stage requires a chart reference")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	loaded, err := e.charts.LocateChart(chartRef, bundle.GetChartVersion(), e.plainHTTP)
	if err != nil {
		return "", fmt.Errorf("render stage: %w", err)
	}
	values := command.GetValues()
	result, err := helmengine.RenderPreflight(ctx, helmengine.RenderOptions{
		ReleaseName:  command.GetReleaseName(),
		Namespace:    command.GetNamespace(),
		Chart:        loaded,
		ChartDigest:  bundle.GetChartDigest(),
		Values:       values,
		ValuesDigest: digestOf(values),
		ValuesPatch:  command.GetValuesPatch(),
		// No CapabilitiesSnapshot: the render stage renders against Helm's
		// defaults. Cluster-aware validation is the cluster stage's job, and
		// inventing a snapshot here would make the two stages disagree.
	})
	if err != nil {
		return "", fmt.Errorf("render stage: %w", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("render stage: encode result: %w", err)
	}
	e.logger.Info("preflight render stage passed",
		"operation_id", command.GetOperationId(), "render_digest", result.RenderDigest)
	return string(encoded), nil
}

// digestOf is the values digest the render options require. The orchestrator
// sends the approved document, not its digest, so the stage computes the same
// digest the renderer records.
func digestOf(values []byte) string {
	sum := sha256.Sum256(values)
	return "sha256:" + hex.EncodeToString(sum[:])
}

var _ StageExecutor = (*RenderStageExecutor)(nil)
