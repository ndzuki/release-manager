package helmengine

import (
	"errors"

	"helm.sh/helm/v3/pkg/chart"
)

// Render error codes are stable persistence and API values for preflight failures.
const (
	RenderCodeValuesSchemaFailed    = "values_schema_failed"
	RenderCodeRenderFailed          = "render_failed"
	RenderCodeDeprecatedAPI         = "deprecated_api"
	RenderCodeSecretOutputForbidden = "secret_output_forbidden"
	RenderCodeSizeExceeded          = "size_exceeded"
	RenderCodeCancelled             = "cancelled"
)

var (
	ErrValuesSchemaFailed    = errors.New("helm: values schema failed")
	ErrDeprecatedAPI         = errors.New("helm: deprecated api")
	ErrSecretOutputForbidden = errors.New("helm: secret output forbidden")
	ErrSizeExceeded          = errors.New("helm: render size exceeded")
)

// RenderError exposes a stable code while retaining the internal error chain.
type RenderError struct {
	Code string `json:"code"`
	Err  error  `json:"-"`
}

func (e *RenderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

func (e *RenderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// CapabilitiesSnapshot is the Kubernetes discovery state captured before render.
type CapabilitiesSnapshot struct {
	KubeVersion string   `json:"kube_version"`
	APIVersions []string `json:"api_versions,omitempty"`
}

// ImageOverride replaces an image reference at a JSON-style values path.
type ImageOverride struct {
	Path  string `json:"path"`
	Image string `json:"image"`
}

// RenderOptions contains only verified local inputs. Render never needs a Kubernetes client.
type RenderOptions struct {
	ReleaseName      string
	Namespace        string
	Chart            *chart.Chart
	ChartDigest      string
	Values           []byte
	ValuesDigest     string
	ValuesPatch      []byte
	ImageOverrides   []ImageOverride
	Capabilities     CapabilitiesSnapshot
	MaxManifestBytes int64
	IncludeCRDs      bool
}

// ResourceSummary is the safe, persistable identity of one rendered resource.
type ResourceSummary struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

// RenderResult is the only render payload safe to persist.
type RenderResult struct {
	RenderDigest string            `json:"render_digest"`
	Resources    []ResourceSummary `json:"resources"`
	Warnings     []string          `json:"warnings,omitempty"`
}
