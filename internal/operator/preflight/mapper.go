package preflight

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// GKVMapper resolves a GroupKind (or GVK) to a GroupVersionResource using
// cached discovery from the target API server.
type GKVMapper struct {
	mapper meta.ResettableRESTMapper
	client dynamic.Interface
}

// NewGKVMapper creates a mapper backed by a live or fake dynamic client and
// REST config for deferred discovery.
func NewGKVMapper(cfg *rest.Config, client dynamic.Interface) (*GKVMapper, error) {
	discoveryClient, err := newDiscoveryClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create discovery client: %w", err)
	}
	cachedDiscovery := memory.NewMemCacheClient(discoveryClient)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDiscovery)

	return &GKVMapper{
		mapper: mapper,
		client: client,
	}, nil
}

// Map resolves a GVK to a GVR and scope, or returns api_not_supported.
func (m *GKVMapper) Map(gvk schema.GroupVersionKind) (schema.GroupVersionResource, bool, error) {
	mapping, err := m.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		if meta.IsNoMatchError(err) {
			return schema.GroupVersionResource{}, false,
				fmt.Errorf("%s: %s: %w", ErrAPINotSupported, ErrAPINotSupported, err)
		}
		return schema.GroupVersionResource{}, false,
			fmt.Errorf("rest mapping for %s: %w", gvk, err)
	}

	namespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace
	return mapping.Resource, namespaced, nil
}

// ResourceClient returns a dynamic ResourceInterface for the GVR, scoped to
// namespace when namespaced.
func (m *GKVMapper) ResourceClient(gvr schema.GroupVersionResource, namespace string) dynamic.ResourceInterface {
	resource := m.client.Resource(gvr)
	if namespace != "" {
		return resource.Namespace(namespace)
	}
	return resource
}

// Reset flushes cached discovery data so the next Map call refreshes from
// the API server.
func (m *GKVMapper) Reset() {
	m.mapper.Reset()
}

// newDiscoveryClient is a testable factory for creating a discovery client.
var newDiscoveryClient = func(cfg *rest.Config) (discovery.DiscoveryInterface, error) {
	return discovery.NewDiscoveryClientForConfig(cfg)
}

// GKVMapperWithFake creates a mapper suitable for testing with a fake
// dynamic client and a provided RESTMapper.
func GKVMapperWithFake(client dynamic.Interface, mapper meta.RESTMapper) *GKVMapper {
	return &GKVMapper{
		mapper: staticMapper{mapper},
		client: client,
	}
}

type staticMapper struct {
	meta.RESTMapper
}

func (s staticMapper) Reset() {}
