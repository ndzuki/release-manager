package operator

import (
	"context"
	"encoding/json"
	"testing"

	"helm.sh/helm/v3/pkg/chart"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
)

// fakeChartLocator records the reference it was asked for and returns a minimal
// chart, so the test proves the stage renders without a cluster.
type fakeChartLocator struct {
	ref, version string
	calls        int
	err          error
}

func (f *fakeChartLocator) LocateChart(ref, version string, _ bool) (*chart.Chart, error) {
	f.calls++
	f.ref, f.version = ref, version
	if f.err != nil {
		return nil, f.err
	}
	return &chart.Chart{
		Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.0.0"},
		Templates: []*chart.File{{
			Name: "templates/configmap.yaml",
			Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\n"),
		}},
	}, nil
}

func renderStageCommand() *operatorv1.Command {
	return &operatorv1.Command{
		CommandId: "cmd-1", OperationId: "op-1", Stage: "render",
		Namespace: "default", ReleaseName: "app",
		Values: []byte("{}"),
		Bundle: &commonv1.ReleaseBundle{
			ChartRef: "oci://reg/charts/app", ChartVersion: "1.0.0", ChartDigest: "sha256:chart",
		},
	}
}

// TASK-114 AC 2: the render stage renders the approved chart and values and
// reports the safe render result. It locates the chart through the seam and
// never needs a cluster.
func TestRenderStageRendersTheChart(t *testing.T) {
	locator := &fakeChartLocator{}
	executor := NewRenderStageExecutor(locator, false, nil)

	result, err := executor.ExecuteStage(context.Background(), renderStageCommand())
	require.NoError(t, err)

	assert.Equal(t, 1, locator.calls)
	assert.Equal(t, "oci://reg/charts/app", locator.ref)
	assert.Equal(t, "1.0.0", locator.version)

	// The reported result is the renderer's safe summary, not a manifest.
	var decoded struct {
		RenderDigest string `json:"render_digest"`
		Resources    []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"resources"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &decoded))
	assert.NotEmpty(t, decoded.RenderDigest)
	require.NotEmpty(t, decoded.Resources)
	assert.Equal(t, "ConfigMap", decoded.Resources[0].Kind)
	assert.NotContains(t, result, "metadata:\n", "the stage result must not carry the raw manifest")
}

// A command without a bundle or chart reference is rejected before any work.
func TestRenderStageRejectsIncompleteCommands(t *testing.T) {
	executor := NewRenderStageExecutor(&fakeChartLocator{}, false, nil)

	noBundle := renderStageCommand()
	noBundle.Bundle = nil
	_, err := executor.ExecuteStage(context.Background(), noBundle)
	assert.Error(t, err)

	noRef := renderStageCommand()
	noRef.Bundle.ChartRef = ""
	_, err = executor.ExecuteStage(context.Background(), noRef)
	assert.Error(t, err)

	_, err = executor.ExecuteStage(context.Background(), nil)
	assert.Error(t, err)
}

// A locator failure surfaces as a stage failure, not a silent pass.
func TestRenderStagePropagatesALocatorFailure(t *testing.T) {
	executor := NewRenderStageExecutor(&fakeChartLocator{err: assert.AnError}, false, nil)
	_, err := executor.ExecuteStage(context.Background(), renderStageCommand())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "render stage")
}
