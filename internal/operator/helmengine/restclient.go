package helmengine

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"net/http"
)

type contextRoundTripper struct {
	ctx  context.Context
	next http.RoundTripper
}

func (t contextRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return t.next.RoundTrip(request.Clone(t.ctx))
}

func withRequestContext(ctx context.Context, config *rest.Config) *rest.Config {
	configCopy := rest.CopyConfig(config)
	configCopy.Wrap(func(next http.RoundTripper) http.RoundTripper {
		return contextRoundTripper{ctx: ctx, next: next}
	})
	return configCopy
}

type restClientGetter struct {
	config    *rest.Config
	namespace string
}

func newRESTClientGetter(config *rest.Config, namespace string) *restClientGetter {
	return &restClientGetter{
		config:    rest.CopyConfig(config),
		namespace: namespace,
	}
}

func (g *restClientGetter) ToRESTConfig() (*rest.Config, error) {
	return rest.CopyConfig(g.config), nil
}

func (g *restClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	client, err := discovery.NewDiscoveryClientForConfig(g.config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes discovery client: %w", err)
	}
	return memory.NewMemCacheClient(client), nil
}

func (g *restClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	discoveryClient, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient), nil
}

func (g *restClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	config := clientcmdapi.NewConfig()
	config.Clusters["cluster"] = &clientcmdapi.Cluster{Server: g.config.Host}
	config.Contexts["context"] = &clientcmdapi.Context{
		Cluster:   "cluster",
		Namespace: g.namespace,
	}
	config.CurrentContext = "context"
	return clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{})
}
