package helmengine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	helmtemplate "helm.sh/helm/v3/pkg/engine"
)

// RenderPreflight renders a verified chart using only local Helm SDK APIs.
// It never creates a Kubernetes client, release storage, or subprocess.
func RenderPreflight(ctx context.Context, opts RenderOptions) (*RenderResult, error) {
	if err := validateRenderOptions(opts); err != nil {
		return nil, err
	}
	if err := renderContextError(ctx); err != nil {
		return nil, err
	}

	values, err := mergeRenderValues(opts.Values, opts.ValuesPatch, opts.ImageOverrides)
	if err != nil {
		return nil, &RenderError{Code: RenderCodeRenderFailed, Err: fmt.Errorf("merge render values: %w", err)}
	}
	if err := renderContextError(ctx); err != nil {
		return nil, err
	}

	chartCopy := cloneChart(opts.Chart)
	if err := chartutil.ProcessDependenciesWithMerge(chartCopy, values); err != nil {
		return nil, &RenderError{Code: RenderCodeRenderFailed, Err: fmt.Errorf("process chart dependencies: %w", err)}
	}

	capabilities, err := renderCapabilities(opts.Capabilities)
	if err != nil {
		return nil, &RenderError{Code: RenderCodeRenderFailed, Err: err}
	}
	valuesToRender, err := chartutil.ToRenderValuesWithSchemaValidation(
		chartCopy,
		values,
		chartutil.ReleaseOptions{
			Name:      opts.ReleaseName,
			Namespace: opts.Namespace,
			Revision:  1,
			IsInstall: true,
		},
		capabilities,
		false,
	)
	if err != nil {
		return nil, &RenderError{Code: RenderCodeValuesSchemaFailed, Err: fmt.Errorf("%w: %v", ErrValuesSchemaFailed, err)}
	}
	if err := renderContextError(ctx); err != nil {
		return nil, err
	}

	rendered, err := helmtemplate.Render(chartCopy, valuesToRender)
	if err != nil {
		return nil, &RenderError{Code: RenderCodeRenderFailed, Err: fmt.Errorf("%w: %v", ErrRenderFailed, err)}
	}
	if opts.IncludeCRDs {
		addCRDs(rendered, chartCopy)
	}
	if err := renderContextError(ctx); err != nil {
		return nil, err
	}

	resources, warnings, err := summarizeRenderedManifests(rendered, opts.Namespace, opts.MaxManifestBytes)
	if err != nil {
		return nil, err
	}
	digest, err := renderDigest(opts, values, resources, warnings)
	if err != nil {
		return nil, &RenderError{Code: RenderCodeRenderFailed, Err: err}
	}

	return &RenderResult{
		RenderDigest: digest,
		Resources:    resources,
		Warnings:     warnings,
	}, nil
}

func validateRenderOptions(opts RenderOptions) error {
	if opts.Chart == nil || opts.Chart.Metadata == nil {
		return &RenderError{Code: RenderCodeRenderFailed, Err: fmt.Errorf("%w: chart is required", ErrRenderFailed)}
	}
	if opts.ReleaseName == "" {
		return &RenderError{Code: RenderCodeRenderFailed, Err: fmt.Errorf("%w: release name is required", ErrRenderFailed)}
	}
	if opts.Namespace == "" {
		return &RenderError{Code: RenderCodeRenderFailed, Err: fmt.Errorf("%w: namespace is required", ErrRenderFailed)}
	}
	if opts.ChartDigest == "" {
		return &RenderError{Code: RenderCodeRenderFailed, Err: fmt.Errorf("%w: verified chart digest is required", ErrRenderFailed)}
	}
	if opts.ValuesDigest == "" {
		return &RenderError{Code: RenderCodeRenderFailed, Err: fmt.Errorf("%w: approved values digest is required", ErrRenderFailed)}
	}
	if opts.MaxManifestBytes < 0 {
		return &RenderError{Code: RenderCodeRenderFailed, Err: fmt.Errorf("%w: max manifest bytes must not be negative", ErrRenderFailed)}
	}
	return nil
}

func renderContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return &RenderError{Code: RenderCodeCancelled, Err: errors.Join(ErrCancelled, err)}
	}
	return nil
}

func renderCapabilities(snapshot CapabilitiesSnapshot) (*chartutil.Capabilities, error) {
	capabilities := chartutil.DefaultCapabilities.Copy()
	if snapshot.KubeVersion != "" {
		version, err := chartutil.ParseKubeVersion(snapshot.KubeVersion)
		if err != nil {
			return nil, fmt.Errorf("parse Kubernetes version %q: %w", snapshot.KubeVersion, err)
		}
		capabilities.KubeVersion = *version
	}

	apiVersions := append([]string(nil), snapshot.APIVersions...)
	sort.Strings(apiVersions)
	capabilities.APIVersions = append(chartutil.VersionSet(nil), apiVersions...)
	return capabilities, nil
}

func addCRDs(rendered map[string]string, chrt *chart.Chart) {
	for _, crd := range chrt.CRDObjects() {
		if crd.File == nil || len(crd.File.Data) == 0 {
			continue
		}
		rendered[crd.Filename] = string(crd.File.Data)
	}
}

func cloneChart(source *chart.Chart) *chart.Chart {
	if source == nil {
		return nil
	}

	cloned := &chart.Chart{
		Metadata:  cloneMetadata(source.Metadata),
		Lock:      cloneLock(source.Lock),
		Raw:       cloneFiles(source.Raw),
		Templates: cloneFiles(source.Templates),
		Values:    cloneValues(source.Values),
		Schema:    append([]byte(nil), source.Schema...),
		Files:     cloneFiles(source.Files),
	}
	dependencies := source.Dependencies()
	if len(dependencies) > 0 {
		clonedDependencies := make([]*chart.Chart, 0, len(dependencies))
		for _, dependency := range dependencies {
			clonedDependencies = append(clonedDependencies, cloneChart(dependency))
		}
		cloned.SetDependencies(clonedDependencies...)
	}
	return cloned
}

func cloneMetadata(source *chart.Metadata) *chart.Metadata {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.Sources = append([]string(nil), source.Sources...)
	cloned.Keywords = append([]string(nil), source.Keywords...)
	cloned.Maintainers = make([]*chart.Maintainer, 0, len(source.Maintainers))
	for _, maintainer := range source.Maintainers {
		if maintainer == nil {
			cloned.Maintainers = append(cloned.Maintainers, nil)
			continue
		}
		maintainerCopy := *maintainer
		cloned.Maintainers = append(cloned.Maintainers, &maintainerCopy)
	}
	if source.Annotations != nil {
		cloned.Annotations = make(map[string]string, len(source.Annotations))
		for key, value := range source.Annotations {
			cloned.Annotations[key] = value
		}
	}
	cloned.Dependencies = cloneDependencies(source.Dependencies)
	return &cloned
}

func cloneDependencies(source []*chart.Dependency) []*chart.Dependency {
	if source == nil {
		return nil
	}
	cloned := make([]*chart.Dependency, 0, len(source))
	for _, dependency := range source {
		if dependency == nil {
			cloned = append(cloned, nil)
			continue
		}
		dependencyCopy := *dependency
		dependencyCopy.Tags = append([]string(nil), dependency.Tags...)
		dependencyCopy.ImportValues = cloneJSONSlice(dependency.ImportValues)
		cloned = append(cloned, &dependencyCopy)
	}
	return cloned
}

func cloneLock(source *chart.Lock) *chart.Lock {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.Dependencies = cloneDependencies(source.Dependencies)
	return &cloned
}

func cloneFiles(source []*chart.File) []*chart.File {
	if source == nil {
		return nil
	}
	cloned := make([]*chart.File, 0, len(source))
	for _, file := range source {
		if file == nil {
			cloned = append(cloned, nil)
			continue
		}
		cloned = append(cloned, &chart.File{Name: file.Name, Data: append([]byte(nil), file.Data...)})
	}
	return cloned
}

func cloneValues(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(source))
	for key, value := range source {
		cloned[key] = cloneJSONValue(value)
	}
	return cloned
}

func cloneJSONSlice(source []interface{}) []interface{} {
	if source == nil {
		return nil
	}
	cloned := make([]interface{}, len(source))
	for index, value := range source {
		cloned[index] = cloneJSONValue(value)
	}
	return cloned
}

func cloneJSONValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		cloned := make(map[string]interface{}, len(typed))
		for key, nested := range typed {
			cloned[key] = cloneJSONValue(nested)
		}
		return cloned
	case []interface{}:
		return cloneJSONSlice(typed)
	case []string:
		return append([]string(nil), typed...)
	case string:
		return strings.Clone(typed)
	default:
		return typed
	}
}
