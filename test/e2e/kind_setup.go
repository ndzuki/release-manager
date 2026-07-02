//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"time"

	"sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cmd"
)

const (
	defaultKindNodeImage = "kindest/node:v1.32.0"
)

var kindProvider = cluster.NewProvider(
	cluster.ProviderWithLogger(cmd.NewLogger()),
)

// createKindCluster creates a kind cluster with NodePort mappings for
// registry (30500), manager HTTP (30080), and manager gRPC (30443).
func createKindCluster(name string) error {
	// Build config YAML dynamically
	config := fmt.Sprintf(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 30500
        hostPort: 30500
        protocol: TCP
      - containerPort: 30080
        hostPort: 30080
        protocol: TCP
      - containerPort: 30443
        hostPort: 30443
        protocol: TCP
`)

	return kindProvider.Create(
		name,
		cluster.CreateWithRawConfig([]byte(config)),
		cluster.CreateWithNodeImage(defaultKindNodeImage),
		cluster.CreateWithWaitForReady(5*time.Minute),
	)
}

// deleteKindCluster deletes a kind cluster by name.
func deleteKindCluster(name string) error {
	return kindProvider.Delete(name, "")
}

// kindKubeconfig returns the kubeconfig path for a kind cluster.
func kindKubeconfig(name string) (string, error) {
	p, err := kindProvider.KubeConfig(name, false)
	if err != nil {
		return "", fmt.Errorf("get kubeconfig for %s: %w", name, err)
	}
	// Write to temp file
	f, err := os.CreateTemp("", "kind-kubeconfig-*")
	if err != nil {
		return "", fmt.Errorf("create kubeconfig temp file: %w", err)
	}
	kubeconfigPath := f.Name()
	if _, err := f.Write([]byte(p)); err != nil {
		f.Close()
		os.Remove(kubeconfigPath)
		return "", fmt.Errorf("write kubeconfig: %w", err)
	}
	f.Close()
	return kubeconfigPath, nil
}

// kindClusterExists checks if a kind cluster with the given name exists.
func kindClusterExists(name string) (bool, error) {
	clusters, err := kindProvider.List()
	if err != nil {
		return false, fmt.Errorf("list kind clusters: %w", err)
	}
	for _, c := range clusters {
		if c == name {
			return true, nil
		}
	}
	return false, nil
}
