package helmengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// REQ-077 Q1: ExtractWorkloads pulls the four-GVR workload identities out of
// a rendered manifest without retaining Secret bodies or other sensitive
// document content.
func TestExtractWorkloads_FourGVRsAndSecretExcluded(t *testing.T) {
	manifest := `apiVersion: v1
kind: Secret
metadata:
  name: app-credentials
  namespace: app
type: Opaque
stringData:
  password: hunter2
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: app
spec:
  replicas: 3
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: db
  namespace: app
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: agent
  namespace: app
---
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate
  namespace: app
---
apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: app
`
	workloads := ExtractWorkloads(manifest, "fallback")

	require.Len(t, workloads, 4)
	assert.Equal(t, []WorkloadSummary{
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "app", Name: "web"},
		{APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: "app", Name: "db"},
		{APIVersion: "apps/v1", Kind: "DaemonSet", Namespace: "app", Name: "agent"},
		{APIVersion: "batch/v1", Kind: "Job", Namespace: "app", Name: "migrate"},
	}, workloads)
}

func TestExtractWorkloads_DefaultNamespaceFallback(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
`
	workloads := ExtractWorkloads(manifest, "release-ns")

	require.Len(t, workloads, 1)
	assert.Equal(t, "release-ns", workloads[0].Namespace)
	assert.Equal(t, "web", workloads[0].Name)
}

func TestExtractWorkloads_EmptyAndMalformedTolerated(t *testing.T) {
	assert.Empty(t, ExtractWorkloads("", "ns"))
	// Malformed documents must not fail the extraction (best-effort).
	assert.Empty(t, ExtractWorkloads("apiVersion: [unterminated", "ns"))
	// Unsupported kinds are skipped; empty namespace falls back to the
	// release namespace for namespaced kinds.
	manifest := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: reader
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: ""
`
	workloads := ExtractWorkloads(manifest, "release-ns")
	require.Len(t, workloads, 1)
	assert.Equal(t, "release-ns", workloads[0].Namespace)
}
