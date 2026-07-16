package preflight

import (
	"bytes"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

// DecodeManifestStream decodes a multi-document YAML/JSON manifest stream
// into a slice of unstructured objects. It skips empty documents and enforces
// resource count and total size limits.
func DecodeManifestStream(manifestData []byte) ([]*unstructured.Unstructured, error) {
	if len(manifestData) == 0 {
		return nil, ErrEmptyManifest
	}
	if len(manifestData) > MaxManifestBytes {
		return nil, ErrOverSizedManifest
	}

	reader := bytes.NewReader(manifestData)
	decoder := k8syaml.NewYAMLOrJSONDecoder(io.NopCloser(reader), 4096)

	var resources []*unstructured.Unstructured

	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode manifest document: %w", err)
		}
		// Skip empty documents (no Kind set).
		if obj.GetKind() == "" {
			continue
		}
		resources = append(resources, &obj)

		if len(resources) > MaxResourceDocs {
			return nil, ErrTooManyResources
		}
	}

	if len(resources) == 0 {
		return nil, ErrEmptyManifest
	}

	return resources, nil
}

// IsNamespaced returns true when a resource is expected to be namespace-scoped
// based on its GVK. Always check this before injecting a namespace.
// Cluster-scoped resources like ClusterRole, ClusterRoleBinding, CRD MUST
// NOT have a namespace.
var clusterScopedResources = map[string]bool{
	"ClusterRole":            true,
	"ClusterRoleBinding":     true,
	"CustomResourceDefinition": true,
	"Namespace":              true,
	"PersistentVolume":       true,
	"StorageClass":           true,
	"PriorityClass":          true,
	"CSIDriver":              true,
	"CSINode":                true,
	"VolumeAttachment":       true,
	"IngressClass":           true,
	"RuntimeClass":           true,
	"MutatingWebhookConfiguration":   true,
	"ValidatingWebhookConfiguration": true,
	"APIService":                    true,
	"SelfSubjectAccessReview":       true,
	"SelfSubjectRulesReview":        true,
	"SubjectAccessReview":           true,
	"TokenReview":                   true,
	"CertificateSigningRequest":     true,
	"Node":                           true,
}

// IsClusterScoped returns true when the resource kind is a known cluster-scoped
// Kubernetes resource.
func IsClusterScoped(kind string) bool {
	return clusterScopedResources[kind]
}
