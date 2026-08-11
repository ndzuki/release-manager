// Package secretmetadata reads Kubernetes Secret metadata without exposing Secret values.
package secretmetadata

import (
	"context"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Secret contains only a Kubernetes Secret name and its sorted data keys.
type Secret struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`
}

// Lister reads Secret metadata from one namespace.
type Lister interface {
	List(context.Context, string) ([]Secret, error)
}

// KubernetesLister implements Lister with client-go.
type KubernetesLister struct {
	client kubernetes.Interface
}

// New creates a Secret metadata lister.
func New(client kubernetes.Interface) *KubernetesLister {
	return &KubernetesLister{client: client}
}

// NewForKubeConfig creates a lister from an explicit kubeconfig, in-cluster
// configuration, or the default client-go loading rules, in that order.
func NewForKubeConfig(kubeConfig string) (*KubernetesLister, error) {
	var (
		config *rest.Config
		err    error
	)
	if kubeConfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
				clientcmd.NewDefaultClientConfigLoadingRules(),
				&clientcmd.ConfigOverrides{},
			).ClientConfig()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("load kubernetes client config: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}
	return New(client), nil
}

// List returns names and sorted data keys. Secret values are never copied to the result.
func (l *KubernetesLister) List(ctx context.Context, namespace string) ([]Secret, error) {
	if l == nil || l.client == nil {
		return nil, fmt.Errorf("kubernetes client is required")
	}
	if namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	items, err := l.client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list secret metadata: %w", err)
	}
	secrets := make([]Secret, 0, len(items.Items))
	for i := range items.Items {
		item := &items.Items[i]
		keys := make([]string, 0, len(item.Data)+len(item.StringData))
		seen := make(map[string]struct{}, len(item.Data)+len(item.StringData))
		for key := range item.Data {
			seen[key] = struct{}{}
		}
		for key := range item.StringData {
			seen[key] = struct{}{}
		}
		for key := range seen {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		secrets = append(secrets, Secret{Name: item.Name, Keys: keys})
	}
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].Name < secrets[j].Name })
	return secrets, nil
}

var _ Lister = (*KubernetesLister)(nil)
