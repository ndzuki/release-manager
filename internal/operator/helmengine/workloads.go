package helmengine

import (
	"errors"
	"io"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// workloadKinds is the four-GVR whitelist observed for rollout progress
// (REQ-077; matches the observer evaluators).
var workloadKinds = map[string]struct{}{
	"Deployment":  {},
	"StatefulSet": {},
	"DaemonSet":   {},
	"Job":         {},
}

// ExtractWorkloads parses a rendered Helm manifest and returns the
// Deployment/StatefulSet/DaemonSet/Job identities it contains. Only
// apiVersion/kind/metadata.name/metadata.namespace are decoded — Secret
// stringData and other manifest bodies are never retained (REQ-077 Q1).
// Malformed or empty documents end extraction (a decoder in error state
// never returns io.EOF, so continuing would loop forever); observation is
// best-effort and must never fail the Helm action that produced the
// manifest.
func ExtractWorkloads(manifest, defaultNamespace string) []WorkloadSummary {
	decoder := yaml.NewDecoder(strings.NewReader(manifest))
	workloads := make([]WorkloadSummary, 0, 1)
	for {
		var identity manifestIdentity
		err := decoder.Decode(&identity)
		if errors.Is(err, io.EOF) {
			return workloads
		}
		if err != nil {
			return workloads
		}
		if _, ok := workloadKinds[identity.Kind]; !ok {
			continue
		}
		if identity.APIVersion == "" || identity.Metadata.Name == "" {
			continue
		}
		namespace := identity.Metadata.Namespace
		if namespace == "" && isNamespacedKind(identity.Kind) {
			namespace = defaultNamespace
		}
		workloads = append(workloads, WorkloadSummary{
			APIVersion: identity.APIVersion,
			Kind:       identity.Kind,
			Namespace:  namespace,
			Name:       identity.Metadata.Name,
		})
	}
}
