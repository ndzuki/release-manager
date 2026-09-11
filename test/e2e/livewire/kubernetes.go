package livewire

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	e2e "github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"
)

// observableWorkloadKinds lists the workload kinds the replica observer reads,
// most specific first. The read-only typed API is used exclusively: no kubectl,
// no shell, no dynamic client.
//
// The stage's ReplicaObserver seam receives only a namespace and a name, so the
// observer probes these kinds in order. If two kinds both resolve, the
// observation is ambiguous and fails closed rather than picking one.
var observableWorkloadKinds = []string{"Deployment", "StatefulSet", "DaemonSet"}

// ReplicaObserver reads the applied replica effect of an emergency change from
// the cluster's own status subresource. It is read-only by construction.
//
// An emergency target names the customer cluster its workload runs in, so the
// observer resolves one client per cluster. An observer built over a single
// client answers for every cluster: that is the management plane, where the
// caller's cluster selection carries no meaning.
type ReplicaObserver struct {
	client   kubernetes.Interface
	provider ClusterClientProvider
}

// ClusterClientProvider resolves a read-only typed client for a cluster name.
type ClusterClientProvider interface {
	ClientFor(cluster string) (kubernetes.Interface, error)
}

// NewReplicaObserver builds the read-only observer over a single typed client.
func NewReplicaObserver(client kubernetes.Interface) (*ReplicaObserver, error) {
	if client == nil {
		return nil, errors.New("livewire: nil kubernetes client")
	}
	return &ReplicaObserver{client: client}, nil
}

// NewClusterReplicaObserver builds an observer that reads every target through
// the client its own cluster resolves to.
func NewClusterReplicaObserver(provider ClusterClientProvider) (*ReplicaObserver, error) {
	if provider == nil {
		return nil, errors.New("livewire: nil cluster client provider")
	}
	return &ReplicaObserver{provider: provider}, nil
}

// clientFor returns the client the named cluster reads through.
func (o *ReplicaObserver) clientFor(cluster string) (kubernetes.Interface, error) {
	if o.provider != nil {
		return o.provider.ClientFor(cluster)
	}
	if o.client == nil {
		return nil, errors.New("livewire: replica observer is unavailable")
	}
	return o.client, nil
}

// ObserveReplicas implements stages.ReplicaObserver.
//
// readyReplicas is reported alongside replicas because a replica-count change
// is only a real applied effect once the pods are ready: a scale-up that never
// becomes ready must not look like success.
func (o *ReplicaObserver) ObserveReplicas(ctx context.Context, cluster, namespace, workloadName string) (stages.ReplicaObservation, error) {
	if o == nil {
		return stages.ReplicaObservation{}, errors.New("livewire: replica observer is unavailable")
	}
	namespace = strings.TrimSpace(namespace)
	workloadName = strings.TrimSpace(workloadName)
	if namespace == "" || workloadName == "" {
		return stages.ReplicaObservation{}, errors.New("livewire: replica observation requires a namespace and a workload name")
	}
	client, err := o.clientFor(strings.TrimSpace(cluster))
	if err != nil {
		return stages.ReplicaObservation{}, err
	}

	type match struct {
		kind  string
		state *workloadReplicaState
	}
	var matches []match
	var readErr error
	for _, kind := range observableWorkloadKinds {
		state, err := readWorkload(ctx, client, kind, namespace, workloadName)
		if err != nil {
			if errors.Is(err, errWorkloadNotFound) {
				continue
			}
			// A transport/permission failure must not be mistaken for an
			// absent workload.
			return stages.ReplicaObservation{}, fmt.Errorf("livewire: observe %s %s/%s: %w", kind, namespace, workloadName, err)
		}
		matches = append(matches, match{kind: kind, state: state})
	}

	switch len(matches) {
	case 0:
		readErr = fmt.Errorf("livewire: no %s named %s/%s", strings.Join(observableWorkloadKinds, ", "), namespace, workloadName)
	case 1:
		state := matches[0].state
		return stages.ReplicaObservation{
			Workload: matches[0].kind + "/" + namespace + "/" + workloadName,
			Replicas: state.replicas,
			Ready:    state.ready,
		}, nil
	default:
		// Two kinds resolved to the same name; the effect cannot be attributed.
		kinds := make([]string, 0, len(matches))
		for _, item := range matches {
			kinds = append(kinds, item.kind)
		}
		readErr = fmt.Errorf("livewire: workload %s/%s is ambiguous across %s", namespace, workloadName, strings.Join(kinds, ", "))
	}
	return stages.ReplicaObservation{}, readErr
}

// workloadReplicaState is the observed replica state of one workload.
type workloadReplicaState struct {
	replicas int32
	ready    int32
}

// errWorkloadNotFound reports a workload that does not exist under one kind. It
// is deliberately distinct from a read failure so the observer can continue
// probing the other kinds without masking a real error.
var errWorkloadNotFound = errors.New("workload not found")

// readWorkload reads one kind's status subresource through the cluster's client.
func readWorkload(ctx context.Context, client kubernetes.Interface, kind, namespace, name string) (*workloadReplicaState, error) {
	switch kind {
	case "Deployment":
		deployment, err := client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, classifyReadError(err)
		}
		return &workloadReplicaState{replicas: deployment.Status.Replicas, ready: deployment.Status.ReadyReplicas}, nil
	case "StatefulSet":
		statefulSet, err := client.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, classifyReadError(err)
		}
		return &workloadReplicaState{replicas: statefulSet.Status.Replicas, ready: statefulSet.Status.ReadyReplicas}, nil
	case "DaemonSet":
		daemonSet, err := client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, classifyReadError(err)
		}
		return &workloadReplicaState{replicas: daemonSet.Status.NumberReady, ready: daemonSet.Status.NumberReady}, nil
	default:
		return nil, fmt.Errorf("livewire: unsupported workload kind %q", kind)
	}
}

// classifyReadError maps the API's not-found response onto the sentinel the
// observer probes with.
func classifyReadError(err error) error {
	if apierrors.IsNotFound(err) {
		return errWorkloadNotFound
	}
	return err
}

// NewRestartProbe builds the client-go restart probe from the configured
// least-privilege restart binding.
func (c *Connector) NewRestartProbe(client kubernetes.Interface) (*stages.KubernetesRestartProbe, error) {
	if c == nil || c.cfg == nil {
		return nil, errUnavailable
	}
	targets := stages.RestartTargets{
		Namespace:   c.cfg.K3d.RestartTargets.Namespace,
		Deployments: append([]string(nil), c.cfg.K3d.RestartTargets.Deployments...),
	}
	return stages.NewKubernetesRestartProbe(client, targets)
}

// NewKubernetesClient builds a typed clientset for the management cluster's
// context. It is the restart probe's client, which acts on the management
// namespace.
//
// The context is selected explicitly rather than taken from the kubeconfig's
// current-context. The dev kubeconfig merges five clusters, so the ambient
// current-context is whichever merged last — a customer cluster. Letting it
// decide would point management reads and writes at a customer cluster.
func NewKubernetesClient(cfg *e2e.Config) (kubernetes.Interface, error) {
	if cfg == nil {
		return nil, errors.New("livewire: nil config")
	}
	return NewKubernetesClientForContext(cfg, cfg.K3d.Context)
}

// NewKubernetesClientForContext builds a typed clientset for one explicit
// kubeconfig context. It is the only place in the E2E harness that reads a
// kubeconfig, so the cluster credential has a single owner.
func NewKubernetesClientForContext(cfg *e2e.Config, contextName string) (kubernetes.Interface, error) {
	if cfg == nil {
		return nil, errors.New("livewire: nil config")
	}
	path := strings.TrimSpace(cfg.K3d.Kubeconfig)
	if path == "" {
		return nil, errors.New("livewire: k3d.kubeconfig is empty")
	}
	contextName = strings.TrimSpace(contextName)
	if contextName == "" {
		return nil, errors.New("livewire: kubeconfig context is empty")
	}
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: path},
		&clientcmd.ConfigOverrides{CurrentContext: contextName},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("livewire: load kubeconfig %s context %s: %w", path, contextName, err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("livewire: build kubernetes client: %w", err)
	}
	return client, nil
}

// ClusterContextsClientProvider resolves one typed client per cluster from the
// declared k3d.cluster_contexts map, building each lazily and caching it, so a
// run keeps a single client per cluster.
type ClusterContextsClientProvider struct {
	cfg     *e2e.Config
	mu      sync.Mutex
	clients map[string]kubernetes.Interface
}

// NewClusterContextsClientProvider builds the provider over the configured map.
func NewClusterContextsClientProvider(cfg *e2e.Config) (*ClusterContextsClientProvider, error) {
	if cfg == nil {
		return nil, errors.New("livewire: nil config")
	}
	if len(cfg.K3d.ClusterContexts) == 0 {
		return nil, errors.New("livewire: k3d.cluster_contexts is empty")
	}
	return &ClusterContextsClientProvider{cfg: cfg, clients: make(map[string]kubernetes.Interface)}, nil
}

// ClientFor implements ClusterClientProvider. An unknown or unset cluster is
// refused rather than falling back to the management plane: reading the wrong
// cluster reports a healthy workload as absent.
func (p *ClusterContextsClientProvider) ClientFor(cluster string) (kubernetes.Interface, error) {
	if p == nil || p.cfg == nil {
		return nil, errors.New("livewire: cluster client provider is unavailable")
	}
	name := strings.TrimSpace(cluster)
	if name == "" {
		return nil, errors.New("livewire: observation target carries no cluster")
	}
	contextName := strings.TrimSpace(p.cfg.K3d.ClusterContexts[name])
	if contextName == "" {
		return nil, fmt.Errorf("livewire: no kubeconfig context declared for cluster %q", name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if client, ok := p.clients[name]; ok {
		return client, nil
	}
	client, err := NewKubernetesClientForContext(p.cfg, contextName)
	if err != nil {
		return nil, err
	}
	p.clients[name] = client
	return client, nil
}
