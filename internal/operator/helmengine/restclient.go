package helmengine

import (
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
)

// restConfigGetter wraps a *rest.Config as a RESTClientGetter for Helm SDK.
// It delegates ToDiscoveryClient and ToRESTMapper to genericclioptions.ConfigFlags.
type restConfigGetter struct {
	*genericclioptions.ConfigFlags
	config *rest.Config
}

// NewRESTClientGetter creates a RESTClientGetter from a *rest.Config.
// The Helm SDK requires a RESTClientGetter for action.Configuration.Init;
// this adapter wraps a *rest.Config with discovery/mapper from
// genericclioptions.ConfigFlags.
func NewRESTClientGetter(config *rest.Config, namespace string) *restConfigGetter {
	cf := genericclioptions.NewConfigFlags(true)
	if namespace != "" {
		cf.Namespace = &namespace
	}
	return &restConfigGetter{ConfigFlags: cf, config: config}
}

// ToRESTConfig returns the provided *rest.Config directly, bypassing
// kubeconfig/file loading.
func (g *restConfigGetter) ToRESTConfig() (*rest.Config, error) {
	return g.config, nil
}
