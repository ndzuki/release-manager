package helmengine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

func TestRenderPreflight_ValuesSchemaFailed(t *testing.T) {
	chartDir := filepath.Join(t.TempDir(), "schema-chart")
	require.NoError(t, os.MkdirAll(filepath.Join(chartDir, "templates"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\nname: schema-chart\nversion: 0.1.0\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "values.schema.json"), []byte(`{"type":"object","required":["replicas"],"properties":{"replicas":{"type":"integer"}}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(chartDir, "templates", "configmap.yaml"), []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\n"), 0o644))
	chrt, err := loader.Load(chartDir)
	require.NoError(t, err)

	_, err = RenderPreflight(t.Context(), RenderOptions{
		ReleaseName:  "release",
		Namespace:    "default",
		Chart:        chrt,
		ChartDigest:  "sha256:chart",
		Values:       []byte(`{"replicas":"wrong"}`),
		ValuesDigest: "sha256:values",
		Capabilities: CapabilitiesSnapshot{KubeVersion: "1.30.0", APIVersions: []string{"v1"}},
	})
	var renderErr *RenderError
	require.ErrorAs(t, err, &renderErr)
	assert.Equal(t, RenderCodeValuesSchemaFailed, renderErr.Code)
	assert.ErrorIs(t, err, ErrValuesSchemaFailed)
}

func TestRenderPreflight_SecretSummaryExcludesData(t *testing.T) {
	chrt := &chart.Chart{
		Metadata:  &chart.Metadata{Name: "secret-chart", Version: "0.1.0", APIVersion: "v2"},
		Templates: []*chart.File{{Name: "secret.yaml", Data: []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: {{ .Release.Name }}\ndata:\n  password: c2VjcmV0\nstringData:\n  token: secret\n")}},
	}
	result, err := RenderPreflight(t.Context(), RenderOptions{
		ReleaseName:  "release",
		Namespace:    "default",
		Chart:        chrt,
		ChartDigest:  "sha256:chart",
		Values:       []byte(`{}`),
		ValuesDigest: "sha256:values",
	})
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	assert.Equal(t, "Secret", result.Resources[0].Kind)
	assert.NotContains(t, mustMarshalJSON(t, result), "c2VjcmV0")
	assert.NotContains(t, mustMarshalJSON(t, result), "stringData")
}

func TestRenderPreflight_DigestStableAcrossInputOrder(t *testing.T) {
	chrt := renderFixtureChart(t)
	left := RenderOptions{
		ReleaseName: "release", Namespace: "default", Chart: chrt, ChartDigest: "sha256:chart", Values: []byte(`{"b":2,"a":1}`), ValuesDigest: "sha256:values",
		ImageOverrides: []ImageOverride{{Path: "image.tag", Image: "1"}, {Path: "image.repository", Image: "example/app"}},
		Capabilities:   CapabilitiesSnapshot{KubeVersion: "1.30.0", APIVersions: []string{"apps/v1", "v1"}},
	}
	right := left
	right.Values = []byte(`{"a":1,"b":2}`)
	right.ImageOverrides = []ImageOverride{{Path: "image.repository", Image: "example/app"}, {Path: "image.tag", Image: "1"}}
	right.Capabilities.APIVersions = []string{"v1", "apps/v1"}
	first, err := RenderPreflight(t.Context(), left)
	require.NoError(t, err)
	second, err := RenderPreflight(t.Context(), right)
	require.NoError(t, err)
	assert.Equal(t, first.RenderDigest, second.RenderDigest)
}

func TestRenderPreflight_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RenderPreflight(ctx, RenderOptions{Chart: renderFixtureChart(t), ReleaseName: "release", Namespace: "default", ChartDigest: "sha256:chart", ValuesDigest: "sha256:values"})
	assert.ErrorIs(t, err, context.Canceled)
	var renderErr *RenderError
	require.ErrorAs(t, err, &renderErr)
	assert.Equal(t, RenderCodeCancelled, renderErr.Code)
}

func renderFixtureChart(t *testing.T) *chart.Chart {
	t.Helper()
	return &chart.Chart{
		Metadata:  &chart.Metadata{Name: "fixture", Version: "0.1.0", APIVersion: "v2"},
		Templates: []*chart.File{{Name: "configmap.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\n")}},
	}
}

func mustMarshalJSON(t *testing.T, value interface{}) string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return string(data)
}

// TASK-114: the cluster preflight stage renders for itself and needs the parsed
// objects, which RenderResult deliberately does not carry (AC-046-02). The
// rendered files come back beside the same result RenderPreflight returns.
func TestRenderManifestsReturnsTheRenderedFiles(t *testing.T) {
	opts := RenderOptions{
		ReleaseName: "app", Namespace: "default", Chart: mustLoadChart(t, writeTestChart(t)),
		ChartDigest: "sha256:chart", ValuesDigest: "sha256:values",
		Values: []byte("{}"),
	}

	rendered, result, err := RenderManifests(t.Context(), opts)
	require.NoError(t, err)

	// The public result is exactly what RenderPreflight returns: no manifest.
	preflight, err := RenderPreflight(t.Context(), opts)
	require.NoError(t, err)
	assert.Equal(t, preflight.RenderDigest, result.RenderDigest)
	assert.Equal(t, preflight.Resources, result.Resources)

	// And the rendered files are available for the caller to decode.
	assert.NotEmpty(t, rendered, "the rendered files must be returned")
}

// A positive MaxManifestBytes is what makes the size check reachable at all
// (summarizeRenderedManifests only enforces it when maxBytes > 0). The render
// stage passes DefaultMaxManifestBytes so a bundle the cluster stage would
// refuse to decode cannot pass preflight; without it REQ-046's size_exceeded
// was unreachable on the production path.
func TestRenderPreflight_SizeExceededIsEnforced(t *testing.T) {
	t.Parallel()

	_, err := RenderPreflight(t.Context(), RenderOptions{
		Chart:            renderFixtureChart(t),
		ReleaseName:      "release",
		Namespace:        "default",
		ChartDigest:      "sha256:chart",
		ValuesDigest:     "sha256:values",
		MaxManifestBytes: 1, // every rendered manifest exceeds a single byte
	})
	require.Error(t, err)

	var renderErr *RenderError
	require.ErrorAs(t, err, &renderErr)
	assert.Equal(t, RenderCodeSizeExceeded, renderErr.Code)
}

// The default limit must be positive, or the check above is silently disabled
// again (the production path passes this constant).
func TestDefaultMaxManifestBytesIsPositive(t *testing.T) {
	t.Parallel()
	assert.Positive(t, DefaultMaxManifestBytes)
}
