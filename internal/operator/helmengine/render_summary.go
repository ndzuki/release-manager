package helmengine

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

type manifestIdentity struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
}

func summarizeRenderedManifests(rendered map[string]string, defaultNamespace string, maxBytes int64) ([]ResourceSummary, []string, error) {
	filenames := renderedManifestFilenames(rendered)
	resources := make([]ResourceSummary, 0, len(filenames))
	warnings := make([]string, 0)
	var totalBytes int64

	for _, filename := range filenames {
		content := rendered[filename]
		totalBytes += int64(len(content))
		if maxBytes > 0 && totalBytes > maxBytes {
			return nil, nil, &RenderError{Code: RenderCodeSizeExceeded, Err: ErrSizeExceeded}
		}
		fileResources, fileWarnings, err := summarizeManifestFile(filename, content, defaultNamespace)
		if err != nil {
			return nil, nil, err
		}
		resources = append(resources, fileResources...)
		warnings = append(warnings, fileWarnings...)
	}

	sortResourceSummaries(resources)
	sort.Strings(warnings)
	return resources, warnings, nil
}

func renderedManifestFilenames(rendered map[string]string) []string {
	filenames := make([]string, 0, len(rendered))
	for filename := range rendered {
		if !strings.HasSuffix(filename, "NOTES.txt") {
			filenames = append(filenames, filename)
		}
	}
	sort.Strings(filenames)
	return filenames
}

func summarizeManifestFile(filename, content, defaultNamespace string) ([]ResourceSummary, []string, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	resources := make([]ResourceSummary, 0, 1)
	warnings := make([]string, 0)
	for document := 1; ; document++ {
		var identity manifestIdentity
		err := decoder.Decode(&identity)
		if errors.Is(err, io.EOF) {
			return resources, warnings, nil
		}
		if err != nil {
			return nil, nil, &RenderError{Code: RenderCodeRenderFailed, Err: fmt.Errorf("decode rendered manifest %s document %d: %w", filename, document, err)}
		}
		if identity.APIVersion == "" && identity.Kind == "" && identity.Metadata.Name == "" {
			continue
		}
		if identity.APIVersion == "" || identity.Kind == "" || identity.Metadata.Name == "" {
			return nil, nil, &RenderError{Code: RenderCodeRenderFailed, Err: fmt.Errorf("rendered manifest %s document %d missing apiVersion, kind, or metadata.name", filename, document)}
		}

		namespace := identity.Metadata.Namespace
		if namespace == "" && isNamespacedKind(identity.Kind) {
			namespace = defaultNamespace
		}
		resources = append(resources, ResourceSummary{
			APIVersion: identity.APIVersion,
			Kind:       identity.Kind,
			Namespace:  namespace,
			Name:       identity.Metadata.Name,
		})
		if isDeprecatedAPIVersion(identity.APIVersion) {
			warnings = append(warnings, RenderCodeDeprecatedAPI+":"+identity.APIVersion+":"+identity.Kind+":"+identity.Metadata.Name)
		}
	}
}

func sortResourceSummaries(resources []ResourceSummary) {
	sort.Slice(resources, func(i, j int) bool {
		left := resources[i]
		right := resources[j]
		if left.APIVersion != right.APIVersion {
			return left.APIVersion < right.APIVersion
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Namespace != right.Namespace {
			return left.Namespace < right.Namespace
		}
		return left.Name < right.Name
	})
}

func isDeprecatedAPIVersion(apiVersion string) bool {
	return strings.Contains(apiVersion, "v1beta1") || strings.Contains(apiVersion, "v1alpha1")
}

func isNamespacedKind(kind string) bool {
	switch kind {
	case "APIService", "ClusterRole", "ClusterRoleBinding", "CustomResourceDefinition", "Namespace", "Node", "PersistentVolume", "PriorityClass", "StorageClass", "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
		return false
	default:
		return true
	}
}

func renderDigest(opts RenderOptions, values map[string]interface{}, resources []ResourceSummary, warnings []string) (string, error) {
	chartName := ""
	chartVersion := ""
	if opts.Chart != nil && opts.Chart.Metadata != nil {
		chartName = opts.Chart.Metadata.Name
		chartVersion = opts.Chart.Metadata.Version
	}

	apiVersions := append([]string(nil), opts.Capabilities.APIVersions...)
	sort.Strings(apiVersions)
	overrides := append([]ImageOverride(nil), opts.ImageOverrides...)
	sort.Slice(overrides, func(i, j int) bool {
		if overrides[i].Path != overrides[j].Path {
			return overrides[i].Path < overrides[j].Path
		}
		return overrides[i].Image < overrides[j].Image
	})

	payload := struct {
		ReleaseName  string                 `json:"release_name"`
		Namespace    string                 `json:"namespace"`
		ChartName    string                 `json:"chart_name"`
		ChartVersion string                 `json:"chart_version"`
		ChartDigest  string                 `json:"chart_digest"`
		ValuesDigest string                 `json:"values_digest"`
		Values       map[string]interface{} `json:"values"`
		Overrides    []ImageOverride        `json:"image_overrides"`
		KubeVersion  string                 `json:"kube_version"`
		APIVersions  []string               `json:"api_versions"`
		IncludeCRDs  bool                   `json:"include_crds"`
		Resources    []ResourceSummary      `json:"resources"`
		Warnings     []string               `json:"warnings"`
	}{
		ReleaseName:  opts.ReleaseName,
		Namespace:    opts.Namespace,
		ChartName:    chartName,
		ChartVersion: chartVersion,
		ChartDigest:  opts.ChartDigest,
		ValuesDigest: opts.ValuesDigest,
		Values:       values,
		Overrides:    overrides,
		KubeVersion:  opts.Capabilities.KubeVersion,
		APIVersions:  apiVersions,
		IncludeCRDs:  opts.IncludeCRDs,
		Resources:    resources,
		Warnings:     warnings,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal render digest input: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest), nil
}
