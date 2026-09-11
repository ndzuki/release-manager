package livewire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	e2e "github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxProbeBody bounds how much of a probe response is drained. The endpoints
// answer with small JSON documents; the limit keeps a misbehaving service from
// streaming an unbounded body into the run.
const maxProbeBody = 1 << 20

// maxEnvironmentBody bounds the /environment document. It is a four-field
// object, so anything larger is a protocol violation rather than a payload.
const maxEnvironmentBody = 64 << 10

// environmentPayload mirrors the GET /environment contract every management
// plane service answers (REQ-065). The E2E harness deliberately imports no
// internal package, so the four wire field names are restated here; the
// control-plane stage is what asserts their values.
type environmentPayload struct {
	Service       string `json:"service"`
	Environment   string `json:"environment"`
	EnvironmentID string `json:"environment_id"`
	Production    bool   `json:"production"`
}

// endpointProbe is one declared control-plane endpoint. name is the declared
// configuration name, not the self-reported service identity: an unreachable
// service must still be attributed to a config entry.
type endpointProbe struct {
	name     string
	endpoint string
}

// ControlPlaneObserver implements stages.ControlPlaneObserver over the declared
// HTTP endpoints plus the operator service's read-only session route.
//
// It observes and never decides: a refused connection, a non-200 probe, an
// unparsable environment document, or an absent operator session all become
// observed values (Healthy=false, Ready=false, empty metadata, offline
// session). Deciding that those values are unacceptable is the control-plane
// stage's job, so a broken environment is reported with the stage's stable
// codes instead of an opaque transport error.
//
// The one deliberate exception is authentication: a run that cannot log in as
// e2e-runner cannot observe anything, so that failure is returned as an error.
type ControlPlaneObserver struct {
	connector  *Connector
	httpClient *http.Client
	probes     []endpointProbe
}

var _ stages.ControlPlaneObserver = (*ControlPlaneObserver)(nil)

// NewControlPlaneObserver builds the observer from an already-validated config
// and the run's shared connector. Every declared endpoint must be present and
// absolute, so a mis-declared control plane fails closed at wiring time rather
// than probing a relative URL.
func NewControlPlaneObserver(cfg *e2e.Config, connector *Connector) (*ControlPlaneObserver, error) {
	if cfg == nil {
		return nil, errors.New("livewire: control-plane observer requires a config")
	}
	if connector == nil || connector.Clients() == nil || connector.Session() == nil {
		return nil, errUnavailable
	}
	probes, err := declaredEndpoints(cfg)
	if err != nil {
		return nil, err
	}
	return &ControlPlaneObserver{
		connector:  connector,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		probes:     probes,
	}, nil
}

// WithHTTPClient injects the transport used for the health, readiness, and
// environment probes.
func (o *ControlPlaneObserver) WithHTTPClient(client *http.Client) *ControlPlaneObserver {
	if o == nil {
		return nil
	}
	if client != nil {
		o.httpClient = client
	}
	return o
}

// ObserveControlPlane implements stages.ControlPlaneObserver.
func (o *ControlPlaneObserver) ObserveControlPlane(ctx context.Context) (stages.ControlPlaneObservation, error) {
	if o == nil || o.connector == nil || o.httpClient == nil {
		return stages.ControlPlaneObservation{}, errUnavailable
	}
	services := make([]stages.ServiceObservation, 0, len(o.probes))
	for _, probe := range o.probes {
		services = append(services, o.observeService(ctx, probe))
	}
	session, err := o.observeOperatorSession(ctx)
	if err != nil {
		return stages.ControlPlaneObservation{}, err
	}
	return stages.ControlPlaneObservation{
		Services:        services,
		OperatorSession: session,
	}, nil
}

// declaredEndpoints returns the six declared control-plane endpoints in a
// stable order. The list mirrors the config schema; an endpoint that is absent
// fails closed because the control-plane stage requires all six services.
func declaredEndpoints(cfg *e2e.Config) ([]endpointProbe, error) {
	declared := []endpointProbe{
		{name: "release_orchestrator", endpoint: cfg.Endpoints.ReleaseOrchestrator},
		{name: "release_webhook", endpoint: cfg.Endpoints.ReleaseWebhook},
		{name: "release_operator", endpoint: cfg.Endpoints.ReleaseOperator},
		{name: "release_auth", endpoint: cfg.Endpoints.ReleaseAuth},
		{name: "release_notifier", endpoint: cfg.Endpoints.ReleaseNotifier},
		{name: "release_api", endpoint: cfg.Endpoints.ReleaseAPI},
	}
	probes := make([]endpointProbe, 0, len(declared))
	for _, probe := range declared {
		probe.endpoint = strings.TrimSpace(probe.endpoint)
		if probe.endpoint == "" {
			return nil, fmt.Errorf("livewire: control-plane endpoint %s is empty", probe.name)
		}
		if _, err := probeURL(probe.endpoint, "health"); err != nil {
			return nil, err
		}
		probes = append(probes, probe)
	}
	return probes, nil
}

// observeService reports what one endpoint answered across the three read-only
// routes.
func (o *ControlPlaneObserver) observeService(ctx context.Context, probe endpointProbe) stages.ServiceObservation {
	return stages.ServiceObservation{
		Name:        probe.name,
		Healthy:     o.probeOK(ctx, probe.endpoint, "health"),
		Ready:       o.probeOK(ctx, probe.endpoint, "readyz"),
		Environment: o.probeEnvironment(ctx, probe.endpoint),
	}
}

// probeOK reports whether one route answered 200. Any other outcome (refused
// transport, non-200 status, malformed endpoint) is observed as false.
func (o *ControlPlaneObserver) probeOK(ctx context.Context, endpoint, path string) bool {
	response, err := o.get(ctx, endpoint, path)
	if err != nil {
		return false
	}
	// The body is drained before it is closed so the transport can reuse the
	// connection; the close error is not actionable once the status is read.
	defer func() { _ = response.Body.Close() }()
	discardBody(response.Body)
	return response.StatusCode == http.StatusOK
}

// probeEnvironment reads the service's self-reported environment metadata. A
// service that cannot answer contributes an empty observation; the control
// plane stage reports the missing metadata against the service name.
func (o *ControlPlaneObserver) probeEnvironment(ctx context.Context, endpoint string) stages.EnvironmentObservation {
	response, err := o.get(ctx, endpoint, "environment")
	if err != nil {
		return stages.EnvironmentObservation{}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return stages.EnvironmentObservation{}
	}
	var payload environmentPayload
	if err := json.NewDecoder(io.LimitReader(response.Body, maxEnvironmentBody)).Decode(&payload); err != nil {
		return stages.EnvironmentObservation{}
	}
	return stages.EnvironmentObservation{
		Service:       payload.Service,
		Environment:   payload.Environment,
		EnvironmentID: payload.EnvironmentID,
		Production:    payload.Production,
	}
}

// discardBody reads and discards a bounded amount of a probe response so the
// transport can reuse the connection. The bound keeps a misbehaving service
// from streaming an unbounded payload into the run.
//
// A read failure is deliberately not propagated: the caller has already
// classified the response by status code, so a failure while discarding cannot
// change the observation, and the caller closes the body regardless.
func discardBody(body io.Reader) {
	if body == nil {
		return
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(body, maxProbeBody)); err != nil {
		return
	}
}

// get performs one probe request. It is the single place the probe URLs are
// built, so every route shares the same endpoint validation.
func (o *ControlPlaneObserver) get(ctx context.Context, endpoint, path string) (*http.Response, error) {
	target, err := probeURL(endpoint, path)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, err
	}
	return o.httpClient.Do(request)
}

// observeOperatorSession reads the runner-visible active operator session. An
// absent session, an unreachable gateway, or a session without an identity is
// reported as an empty observation: "no online session was observed" is the
// fact the control-plane stage needs, and it reports the failure itself.
func (o *ControlPlaneObserver) observeOperatorSession(ctx context.Context) (stages.OperatorSessionObservation, error) {
	if err := o.connector.Login(ctx); err != nil {
		return stages.OperatorSessionObservation{}, err
	}
	session := o.activeOperatorSession(ctx)
	if session == nil {
		return stages.OperatorSessionObservation{}, nil
	}
	status := session.GetStatus()
	return stages.OperatorSessionObservation{
		SessionID:     session.GetSessionId(),
		OperatorID:    session.GetOperatorId(),
		Status:        status,
		LastHeartbeat: formatTimestamp(session.GetLastHeartbeat()),
		Online:        strings.EqualFold(strings.TrimSpace(status), operatorSessionOnline),
	}, nil
}

// activeOperatorSession returns the runner-visible session, or nil when the
// gateway answered without one or could not be reached at all.
//
// A missing session is an observation rather than an error, which is why this
// helper returns a pointer instead of an error: the control-plane stage reports
// the missing identity with its own stable code, and the observer never decides
// that the environment is unusable. Only authentication failure is an error,
// and it is raised by the caller before this read.
func (o *ControlPlaneObserver) activeOperatorSession(ctx context.Context) *operatorv1.OperatorSession {
	response, err := o.connector.Clients().Operator().GetActiveOperatorSession(ctx,
		authorizedRequest(o.connector.Session().Token(), &operatorv1.GetActiveOperatorSessionRequest{}))
	if err != nil || response == nil || response.Msg == nil {
		return nil
	}
	return response.Msg.GetSession()
}

// formatTimestamp renders an optional protobuf timestamp as RFC 3339 UTC. An
// unset or out-of-range timestamp becomes the empty string rather than a
// fabricated epoch value.
func formatTimestamp(ts *timestamppb.Timestamp) string {
	if ts == nil || !ts.IsValid() {
		return ""
	}
	return ts.AsTime().UTC().Format(time.RFC3339Nano)
}

// probeURL appends one read-only route to a declared endpoint.
func probeURL(endpoint, path string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("livewire: invalid endpoint %q: %w", endpoint, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("livewire: endpoint %q must be an absolute http or https URL", endpoint)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("livewire: endpoint %q has no host", endpoint)
	}
	return strings.TrimSuffix(endpoint, "/") + "/" + path, nil
}

// FormalReader implements the read-only inventory and bundle seams over the
// generated Connect clients.
type FormalReader struct {
	connector *Connector
}

var (
	_ stages.InventoryReader = (*FormalReader)(nil)
	_ stages.BundleReader    = (*FormalReader)(nil)
	_ stages.ArtifactReader  = (*FormalReader)(nil)
)

// NewFormalReader builds the read-only adapter over the run's shared connector,
// so reads carry the same single runner identity as the write stages.
func NewFormalReader(connector *Connector) (*FormalReader, error) {
	if connector == nil || connector.Clients() == nil || connector.Session() == nil {
		return nil, errUnavailable
	}
	return &FormalReader{connector: connector}, nil
}

// begin authenticates the run and returns the client bundle.
func (r *FormalReader) begin(ctx context.Context) (*e2e.ClientBundle, error) {
	clients, err := r.connector.clientsOrFail()
	if err != nil {
		return nil, err
	}
	if err := r.connector.Login(ctx); err != nil {
		return nil, err
	}
	return clients, nil
}

// GetBundle implements stages.BundleReader.
//
// Both identifiers are required: the orchestrator rejects a bundle read without
// a release_definition_id, because bundle visibility is definition-scoped
// (real smoke 2026-08-27: "not_authorized: release_definition_id is required").
// Rejecting it here keeps the failure local instead of spending a round trip.
func (r *FormalReader) GetBundle(ctx context.Context, request stages.BundleRequest) (stages.BundleObservation, error) {
	if r == nil || r.connector == nil {
		return stages.BundleObservation{}, errUnavailable
	}
	bundleID := strings.TrimSpace(request.BundleID)
	definitionID := strings.TrimSpace(request.ReleaseDefinitionID)
	if bundleID == "" || definitionID == "" {
		return stages.BundleObservation{}, errors.New("livewire: bundle reads require a bundle id and a release definition id")
	}
	clients, err := r.begin(ctx)
	if err != nil {
		return stages.BundleObservation{}, err
	}
	response, err := clients.Bundle().GetBundle(ctx,
		authorizedRequest(r.connector.Session().Token(), &orchestratorv1.GetBundleRequest{
			BundleId:            bundleID,
			ReleaseDefinitionId: definitionID,
		}))
	if err != nil {
		return stages.BundleObservation{}, fmt.Errorf("livewire: get bundle %s: %w", bundleID, err)
	}
	if response == nil || response.Msg == nil {
		return stages.BundleObservation{}, errors.New("livewire: empty get bundle response")
	}
	summary := response.Msg.GetBundle().GetSummary()
	if summary == nil {
		return stages.BundleObservation{}, fmt.Errorf("livewire: bundle %s has no summary", bundleID)
	}
	return stages.BundleObservation{
		ID:     summary.GetId(),
		Digest: summary.GetDigest().GetValue(),
		Status: summary.GetStatus().String(),
	}, nil
}

// ListReleaseInventory implements stages.InventoryReader.
//
// Parent-scoped reads are issued one parent at a time (clusters per customer,
// definitions per customer, bundles per definition, routes per cluster) instead
// of relying on an empty filter to mean "everything": the bundle routes require
// a release_definition_id outright, and the documented contract for an
// unfiltered list is not a complete enumeration. The per-parent results are
// de-duplicated by stable id because a server that ignores an unsupported
// filter would otherwise re-report shared entities once per parent.
func (r *FormalReader) ListReleaseInventory(ctx context.Context) (stages.InventoryObservation, error) {
	if r == nil || r.connector == nil {
		return stages.InventoryObservation{}, errUnavailable
	}
	clients, err := r.begin(ctx)
	if err != nil {
		return stages.InventoryObservation{}, err
	}
	token := r.connector.Session().Token()

	customers, err := listCustomers(ctx, clients, token)
	if err != nil {
		return stages.InventoryObservation{}, err
	}
	clusters, err := listClusters(ctx, clients, token, customers)
	if err != nil {
		return stages.InventoryObservation{}, err
	}
	definitions, err := listDefinitions(ctx, clients, token, customers)
	if err != nil {
		return stages.InventoryObservation{}, err
	}
	bundles, err := listBundles(ctx, clients, token, definitions)
	if err != nil {
		return stages.InventoryObservation{}, err
	}
	routes, err := listRoutes(ctx, clients, token, clusters)
	if err != nil {
		return stages.InventoryObservation{}, err
	}
	rows, err := listInventoryRows(ctx, clients, token)
	if err != nil {
		return stages.InventoryObservation{}, err
	}

	return stages.InventoryObservation{
		Customers:   identityObservations(customers),
		Clusters:    clusterIdentities(clusters),
		Routes:      routes,
		Definitions: definitionObservations(definitions),
		Bundles:     bundleObservations(bundles),
		Rows:        dedupeRows(rows),
	}, nil
}

// listCustomers reads every customer visible to the runner. Disabled customers
// are included because the expected identity counts are derived from the seed
// manifest, which counts a seeded customer whether or not it was later
// disabled.
func listCustomers(ctx context.Context, clients *e2e.ClientBundle, token string) ([]*commonv1.Customer, error) {
	response, err := clients.Orchestrator().ListCustomers(ctx,
		authorizedRequest(token, &orchestratorv1.ListCustomersRequest{IncludeDisabled: true}))
	if err != nil {
		return nil, fmt.Errorf("livewire: list customers: %w", err)
	}
	if response == nil || response.Msg == nil {
		return nil, errors.New("livewire: empty list customers response")
	}
	return response.Msg.GetCustomers(), nil
}

// clusterRecord is one cluster plus the owning customer, which the wire route
// does not carry.
type clusterRecord struct {
	id         string
	name       string
	customerID string
}

// listClusters reads the clusters of every customer and de-duplicates by id. A
// customer without a stable id is skipped: an unfiltered ListClusters would
// return every cluster, which the per-customer loop already covers.
func listClusters(ctx context.Context, clients *e2e.ClientBundle, token string, customers []*commonv1.Customer) ([]clusterRecord, error) {
	var observed []clusterRecord
	for _, customer := range customers {
		customerID := strings.TrimSpace(customer.GetId())
		if customerID == "" {
			continue
		}
		response, err := clients.Orchestrator().ListClusters(ctx,
			authorizedRequest(token, &orchestratorv1.ListClustersRequest{CustomerId: customerID}))
		if err != nil {
			return nil, fmt.Errorf("livewire: list clusters for customer %s: %w", customerID, err)
		}
		if response == nil || response.Msg == nil {
			return nil, fmt.Errorf("livewire: empty list clusters response for customer %s", customerID)
		}
		for _, cluster := range response.Msg.GetClusters() {
			observed = append(observed, clusterRecord{
				id:         cluster.GetId(),
				name:       cluster.GetName(),
				customerID: cluster.GetCustomerId(),
			})
		}
	}
	return dedupeClusters(observed), nil
}

// listDefinitions reads the release definitions of every customer. Disabled
// definitions are included for the same reason disabled customers are.
func listDefinitions(ctx context.Context, clients *e2e.ClientBundle, token string, customers []*commonv1.Customer) ([]*commonv1.ReleaseDefinition, error) {
	var observed []*commonv1.ReleaseDefinition
	for _, customer := range customers {
		customerID := strings.TrimSpace(customer.GetId())
		if customerID == "" {
			continue
		}
		response, err := clients.Orchestrator().ListReleaseDefinitions(ctx,
			authorizedRequest(token, &orchestratorv1.ListReleaseDefinitionsRequest{
				CustomerId:      customerID,
				IncludeDisabled: true,
			}))
		if err != nil {
			return nil, fmt.Errorf("livewire: list release definitions for customer %s: %w", customerID, err)
		}
		if response == nil || response.Msg == nil {
			return nil, fmt.Errorf("livewire: empty list release definitions response for customer %s", customerID)
		}
		observed = append(observed, response.Msg.GetDefinitions()...)
	}
	return dedupeDefinitions(observed), nil
}

// listBundles reads the bundles bound to each release definition. The release
// definition id is a required filter, so this parent loop is the only complete
// enumeration available.
func listBundles(ctx context.Context, clients *e2e.ClientBundle, token string, definitions []*commonv1.ReleaseDefinition) ([]*orchestratorv1.BundleSummary, error) {
	var observed []*orchestratorv1.BundleSummary
	for _, definition := range definitions {
		definitionID := strings.TrimSpace(definition.GetId())
		if definitionID == "" {
			continue
		}
		response, err := clients.Bundle().ListBundles(ctx,
			authorizedRequest(token, &orchestratorv1.ListBundlesRequest{ReleaseDefinitionId: definitionID}))
		if err != nil {
			return nil, fmt.Errorf("livewire: list bundles for definition %s: %w", definitionID, err)
		}
		if response == nil || response.Msg == nil {
			return nil, fmt.Errorf("livewire: empty list bundles response for definition %s", definitionID)
		}
		observed = append(observed, response.Msg.GetBundles()...)
	}
	return observed, nil
}

// listRoutes reads the artifact routes of every cluster.
//
// ClusterRoute exposes no release-definition identity, so the resulting
// RouteObservation.DefinitionID is necessarily empty: the public API models
// artifact routing per cluster, not per release definition. That matters
// because stages/inventory.go counts a route's DefinitionID when it checks that
// every E2E definition id is observable, so with a non-empty route set the
// inventory stage reports "route.definition_id missing" for each E2E definition
// unless the proto grows that binding. The adapter reports what the API
// publishes rather than inventing a definition binding it cannot see.
func listRoutes(ctx context.Context, clients *e2e.ClientBundle, token string, clusters []clusterRecord) ([]stages.RouteObservation, error) {
	var observed []stages.RouteObservation
	for _, cluster := range clusters {
		clusterID := strings.TrimSpace(cluster.id)
		if clusterID == "" {
			continue
		}
		response, err := clients.Orchestrator().GetClusterRoutes(ctx,
			authorizedRequest(token, &orchestratorv1.GetClusterRoutesRequest{ClusterId: clusterID}))
		if err != nil {
			return nil, fmt.Errorf("livewire: get cluster routes for cluster %s: %w", clusterID, err)
		}
		if response == nil || response.Msg == nil {
			return nil, fmt.Errorf("livewire: empty get cluster routes response for cluster %s", clusterID)
		}
		for _, route := range response.Msg.GetRoutes() {
			observed = append(observed, stages.RouteObservation{
				ID:           route.GetId(),
				CustomerID:   cluster.customerID,
				ClusterID:    route.GetClusterId(),
				DefinitionID: "",
				SourcePrefix: route.GetSourcePrefix(),
			})
		}
	}
	return dedupeRoutes(observed), nil
}

// listInventoryRows reads the release inventory in one unfiltered request: the
// request message has no fields, and the response is documented as the complete
// observed inventory.
func listInventoryRows(ctx context.Context, clients *e2e.ClientBundle, token string) ([]stages.InventoryRow, error) {
	response, err := clients.Orchestrator().ListReleaseInventory(ctx,
		authorizedRequest(token, &orchestratorv1.ListReleaseInventoryRequest{}))
	if err != nil {
		return nil, fmt.Errorf("livewire: list release inventory: %w", err)
	}
	if response == nil || response.Msg == nil {
		return nil, errors.New("livewire: empty list release inventory response")
	}
	rows := make([]stages.InventoryRow, 0, len(response.Msg.GetRows()))
	for _, row := range response.Msg.GetRows() {
		rows = append(rows, stages.InventoryRow{
			CustomerID:   row.GetCustomerId(),
			ClusterID:    row.GetClusterId(),
			DefinitionID: row.GetReleaseDefinitionId(),
			Namespace:    row.GetNamespace(),
			ReleaseName:  row.GetReleaseName(),
			Revision:     row.GetRevision(),
		})
	}
	return rows, nil
}

// identityObservations maps customers onto the identity contract.
func identityObservations(customers []*commonv1.Customer) []stages.IdentityObservation {
	out := make([]stages.IdentityObservation, 0, len(customers))
	for _, customer := range customers {
		out = append(out, stages.IdentityObservation{ID: customer.GetId(), Name: customer.GetName()})
	}
	return dedupeIdentity(out)
}

// clusterIdentities maps clusters onto the identity contract.
func clusterIdentities(clusters []clusterRecord) []stages.IdentityObservation {
	out := make([]stages.IdentityObservation, 0, len(clusters))
	for _, cluster := range clusters {
		out = append(out, stages.IdentityObservation{ID: cluster.id, Name: cluster.name})
	}
	return dedupeIdentity(out)
}

// definitionObservations maps release definitions onto the identity contract.
func definitionObservations(definitions []*commonv1.ReleaseDefinition) []stages.DefinitionObservation {
	out := make([]stages.DefinitionObservation, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, stages.DefinitionObservation{ID: definition.GetId(), Name: definition.GetName()})
	}
	return dedupeDefinitionObservations(out)
}

// bundleObservations maps bundle summaries onto the read-only bundle contract
// and de-duplicates the shared bundles the per-definition loop can repeat.
func bundleObservations(bundles []*orchestratorv1.BundleSummary) []stages.BundleObservation {
	out := make([]stages.BundleObservation, 0, len(bundles))
	for _, bundle := range bundles {
		out = append(out, stages.BundleObservation{
			ID:     bundle.GetId(),
			Digest: bundle.GetDigest().GetValue(),
			Status: bundle.GetStatus().String(),
		})
	}
	return dedupeByIdentity(out, func(item stages.BundleObservation) string { return item.ID })
}

// dedupeByIdentity collapses repeated stable ids while preserving entries that
// carry no id at all: an entity the server did not identify is still an
// observation, and silently dropping it would hide the anomaly.
func dedupeByIdentity[T any](items []T, id func(T) string) []T {
	seen := make(map[string]struct{}, len(items))
	out := make([]T, 0, len(items))
	for _, item := range items {
		key := strings.TrimSpace(id(item))
		if key == "" {
			out = append(out, item)
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func dedupeIdentity(items []stages.IdentityObservation) []stages.IdentityObservation {
	return dedupeByIdentity(items, func(item stages.IdentityObservation) string { return item.ID })
}

func dedupeDefinitionObservations(items []stages.DefinitionObservation) []stages.DefinitionObservation {
	return dedupeByIdentity(items, func(item stages.DefinitionObservation) string { return item.ID })
}

func dedupeClusters(items []clusterRecord) []clusterRecord {
	return dedupeByIdentity(items, func(item clusterRecord) string { return item.id })
}

func dedupeDefinitions(items []*commonv1.ReleaseDefinition) []*commonv1.ReleaseDefinition {
	return dedupeByIdentity(items, func(item *commonv1.ReleaseDefinition) string { return item.GetId() })
}

func dedupeRoutes(items []stages.RouteObservation) []stages.RouteObservation {
	return dedupeByIdentity(items, func(item stages.RouteObservation) string { return item.ID })
}

// dedupeRows collapses inventory rows that repeat the same release identity.
// The composite key is used rather than the definition id alone because a
// definition is bound to one cluster, and two clusters could in principle reuse
// a definition id only if the server allowed it; keeping both would then be a
// visible conflict rather than a silent overwrite.
func dedupeRows(items []stages.InventoryRow) []stages.InventoryRow {
	return dedupeByIdentity(items, func(item stages.InventoryRow) string {
		if item.DefinitionID == "" && item.ClusterID == "" && item.CustomerID == "" {
			return ""
		}
		return item.CustomerID + "|" + item.ClusterID + "|" + item.DefinitionID
	})
}
