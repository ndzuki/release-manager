package livewire

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	e2e "github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// declaredServiceNames is the exact Name set the observer must publish for the
// six declared endpoints, in order.
var declaredServiceNames = []string{
	"release_orchestrator",
	"release_webhook",
	"release_operator",
	"release_auth",
	"release_notifier",
	"release_api",
}

// tcpOnlyServiceName is the one declared endpoint with no HTTP read-only
// surface: the orchestrator's mTLS agent gateway, observed by reachability.
const tcpOnlyServiceName = "release_operator"

// closedEndpoint returns an absolute URL that refuses connections: a listener
// is bound and immediately closed, so the port is free and the refusal is
// deterministic rather than dependent on the host's port allocation.
func closedEndpoint(t *testing.T) string {
	t.Helper()

	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return "http://" + address
}

func TestControlPlaneObserverReportsEveryDeclaredService(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	heartbeat := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	h.operator.setSessions(&operatorv1.OperatorSession{
		SessionId:     "session-1",
		OperatorId:    "operator-1",
		Status:        "ONLINE",
		LastHeartbeat: timestamppb.New(heartbeat),
	})

	observation, err := h.newObserver(t).ObserveControlPlane(context.Background())
	if err != nil {
		t.Fatalf("ObserveControlPlane() error = %v", err)
	}
	if len(observation.Services) != len(declaredServiceNames) {
		t.Fatalf("services = %d, want %d", len(observation.Services), len(declaredServiceNames))
	}
	for index, service := range observation.Services {
		if service.Name != declaredServiceNames[index] {
			t.Fatalf("service[%d].Name = %q, want %q", index, service.Name, declaredServiceNames[index])
		}
		if !service.Healthy {
			t.Fatalf("service %s Healthy = false, want true", service.Name)
		}
		if !service.Ready {
			t.Fatalf("service %s Ready = false, want true", service.Name)
		}
		if service.Name == tcpOnlyServiceName {
			// The mTLS agent gateway has no HTTP read-only surface: it is
			// observed by reachability and reports no environment metadata.
			if service.Transport != stages.TransportTCP {
				t.Fatalf("service %s Transport = %q, want %q", service.Name, service.Transport, stages.TransportTCP)
			}
			if service.Environment != (stages.EnvironmentObservation{}) {
				t.Fatalf("service %s Environment = %+v, want an empty observation", service.Name, service.Environment)
			}
			continue
		}
		want := stages.EnvironmentObservation{
			Service:       "release-orchestrator",
			Environment:   "test",
			EnvironmentID: "livewire-unit",
			Production:    false,
		}
		if service.Environment != want {
			t.Fatalf("service %s Environment = %+v, want %+v", service.Name, service.Environment, want)
		}
	}

	session := observation.OperatorSession
	if session.SessionID != "session-1" || session.OperatorID != "operator-1" {
		t.Fatalf("session identity = %+v, want session-1/operator-1", session)
	}
	if session.Status != "ONLINE" {
		t.Fatalf("session status = %q, want ONLINE", session.Status)
	}
	if !session.Online {
		t.Fatal("session Online = false, want true for an ONLINE session")
	}
	if session.LastHeartbeat != "2026-01-02T03:04:05Z" {
		t.Fatalf("session last heartbeat = %q, want the RFC3339 UTC rendering", session.LastHeartbeat)
	}

	// The control-plane stage is the consumer: a healthy observation with six
	// distinct declared services must pass it unchanged.
	if err := stages.NewControlPlaneStage(h.newObserver(t)).Run(context.Background(), nil); err != nil {
		t.Fatalf("control-plane stage rejected a healthy observation: %v", err)
	}
}

func TestControlPlaneObserverObservesProbeFailuresWithoutError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.setHealth(http.StatusInternalServerError)
	h.setReady(http.StatusServiceUnavailable)
	h.setEnvironment(http.StatusServiceUnavailable, "")

	observation, err := h.newObserver(t).ObserveControlPlane(context.Background())
	if err != nil {
		t.Fatalf("ObserveControlPlane() error = %v, want an observed failure", err)
	}
	for _, service := range observation.Services {
		if service.Name == tcpOnlyServiceName {
			// Reachability is independent of the HTTP probe statuses: the fake
			// listener still accepts connections, so this service stays healthy
			// and only the HTTP services report the injected failures.
			if service.Transport != stages.TransportTCP {
				t.Fatalf("service %s Transport = %q, want %q", service.Name, service.Transport, stages.TransportTCP)
			}
			if !service.Healthy || !service.Ready {
				t.Fatalf("service %s reachability = healthy %t ready %t, want both true",
					service.Name, service.Healthy, service.Ready)
			}
			continue
		}
		if service.Healthy {
			t.Fatalf("service %s Healthy = true, want false", service.Name)
		}
		if service.Ready {
			t.Fatalf("service %s Ready = true, want false", service.Name)
		}
		if service.Environment != (stages.EnvironmentObservation{}) {
			t.Fatalf("service %s Environment = %+v, want an empty observation", service.Name, service.Environment)
		}
	}

	// The stage, not the observer, decides that these observations are fatal.
	err = stages.NewControlPlaneStage(h.newObserver(t)).Run(context.Background(), nil)
	if !errors.Is(err, stages.ErrEnvironmentUnhealthy) {
		t.Fatalf("control-plane stage error = %v, want errors.Is(..., ErrEnvironmentUnhealthy)", err)
	}
}

func TestControlPlaneObserverReportsTransportFailureAsUnhealthyService(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.cfg.Endpoints.ReleaseWebhook = closedEndpoint(t)

	observation, err := h.newObserver(t).ObserveControlPlane(context.Background())
	if err != nil {
		t.Fatalf("ObserveControlPlane() error = %v, want an observed failure", err)
	}
	for _, service := range observation.Services {
		wantHealthy := service.Name != "release_webhook"
		if service.Healthy != wantHealthy {
			t.Fatalf("service %s Healthy = %t, want %t", service.Name, service.Healthy, wantHealthy)
		}
		if service.Name == "release_webhook" && service.Environment != (stages.EnvironmentObservation{}) {
			t.Fatalf("unreachable service reported environment metadata: %+v", service.Environment)
		}
	}

	stageErr := controlPlaneStageError(t, h.newObserver(t))
	if stageErr.Component != "release_webhook" {
		t.Fatalf("stage error component = %q, want release_webhook", stageErr.Component)
	}
}

// controlPlaneStageError runs the control-plane stage and returns its typed
// stage error.
func controlPlaneStageError(t *testing.T, observer stages.ControlPlaneObserver) *stages.StageError {
	t.Helper()

	err := stages.NewControlPlaneStage(observer).Run(context.Background(), nil)
	if err == nil {
		t.Fatal("control-plane stage passed, want a stage error")
	}
	var stageErr *stages.StageError
	if !errors.As(err, &stageErr) {
		t.Fatalf("error = %v (%T), want *stages.StageError", err, err)
	}
	return stageErr
}

func TestControlPlaneObserverRejectsProductionAndPartialEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantDetail string
	}{
		{
			name:       "production environment",
			body:       `{"service":"release-orchestrator","environment":"development","environment_id":"dev-local","production":true}`,
			wantDetail: "production environment rejected",
		},
		{
			name:       "missing metadata",
			body:       `{"service":"release-orchestrator"}`,
			wantDetail: "environment metadata missing",
		},
		{
			name:       "malformed document",
			body:       `{`,
			wantDetail: "environment metadata missing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.setEnvironment(http.StatusOK, test.body)
			h.operator.setSessions(&operatorv1.OperatorSession{SessionId: "s", OperatorId: "o", Status: "online"})

			stageErr := controlPlaneStageError(t, h.newObserver(t))
			if !strings.Contains(stageErr.Detail, test.wantDetail) {
				t.Fatalf("stage error detail = %q, want it to contain %q", stageErr.Detail, test.wantDetail)
			}
		})
	}
}

func TestControlPlaneObserverObservesOfflineOperatorSession(t *testing.T) {
	t.Parallel()

	t.Run("expired status", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.operator.setSessions(&operatorv1.OperatorSession{SessionId: "s", OperatorId: "o", Status: "expired"})

		observation, err := h.newObserver(t).ObserveControlPlane(context.Background())
		if err != nil {
			t.Fatalf("ObserveControlPlane() error = %v", err)
		}
		if observation.OperatorSession.Online {
			t.Fatal("Online = true, want false for an expired session")
		}
		if observation.OperatorSession.Status != "expired" {
			t.Fatalf("Status = %q, want expired", observation.OperatorSession.Status)
		}
		stageErr := controlPlaneStageError(t, h.newObserver(t))
		if stageErr.Component != "operator-session" {
			t.Fatalf("stage error component = %q, want operator-session", stageErr.Component)
		}
	})

	t.Run("session route unavailable", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.operator.setError(connect.NewError(connect.CodeFailedPrecondition, errors.New("no active session")))

		observation, err := h.newObserver(t).ObserveControlPlane(context.Background())
		if err != nil {
			t.Fatalf("ObserveControlPlane() error = %v, want an observed failure", err)
		}
		if observation.OperatorSession != (stages.OperatorSessionObservation{}) {
			t.Fatalf("session = %+v, want an empty observation", observation.OperatorSession)
		}
		stageErr := controlPlaneStageError(t, h.newObserver(t))
		if !strings.Contains(stageErr.Detail, "active session identity missing") {
			t.Fatalf("stage error detail = %q, want a missing-session detail", stageErr.Detail)
		}
	})
}

func TestNewControlPlaneObserverRejectsUnusableEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(cfg *e2e.Config)
	}{
		{name: "empty endpoint", mutate: func(cfg *e2e.Config) { cfg.Endpoints.ReleaseAuth = "" }},
		{name: "relative endpoint", mutate: func(cfg *e2e.Config) { cfg.Endpoints.ReleaseAPI = "localhost:8087" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			test.mutate(h.cfg)
			if _, err := NewControlPlaneObserver(h.cfg, h.connector); err == nil {
				t.Fatal("NewControlPlaneObserver() error = nil, want a fail-closed error")
			}
		})
	}
}

func TestNewControlPlaneObserverRequiresConnector(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	if _, err := NewControlPlaneObserver(h.cfg, nil); !errors.Is(err, errUnavailable) {
		t.Fatalf("NewControlPlaneObserver(nil connector) error = %v, want errUnavailable", err)
	}
}

func TestFormalReaderListReleaseInventoryAggregatesEveryParent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	sharedSummary := &orchestratorv1.BundleSummary{
		Id:     "bundle-1",
		Digest: &commonv1.ReleaseDigest{Algorithm: "sha256", Value: "sha256:abc"},
		Status: commonv1.BundleStatus_BUNDLE_STATUS_VALIDATED,
	}
	h.orch.setInventorySources(
		[]*commonv1.Customer{{Id: "cust-a", Name: "Customer A"}, {Id: "cust-b", Name: "Customer B"}},
		map[string][]*commonv1.Cluster{
			"cust-a": {{Id: "cluster-a", Name: "Cluster A", CustomerId: "cust-a"}},
			"cust-b": {
				{Id: "cluster-b", Name: "Cluster B", CustomerId: "cust-b"},
				{Id: "cluster-c", Name: "Cluster C", CustomerId: "cust-b"},
			},
		},
		map[string][]*commonv1.ReleaseDefinition{
			"cust-a": {{Id: "def-1", Name: "Definition 1"}, {Id: "def-2", Name: "Definition 2"}},
			"cust-b": {},
		},
		map[string][]*orchestratorv1.ClusterRoute{
			"cluster-a": {{Id: "route-1", ClusterId: "cluster-a", SourcePrefix: "docker.io/library/"}},
			"cluster-b": {{Id: "route-2", ClusterId: "cluster-b", SourcePrefix: "https://charts.example.com/"}},
			"cluster-c": {},
		},
	)
	// The same bundle is bound to both definitions: the per-definition loop
	// must report it once.
	h.bundle.setBundle("def-1", sharedSummary, &orchestratorv1.BundleDetail{Summary: sharedSummary})
	h.bundle.setBundle("def-2", sharedSummary, &orchestratorv1.BundleDetail{Summary: sharedSummary})
	h.orch.setInventory(inventoryRow("def-1", 3, nil), inventoryRow("def-2", 1, nil))

	observation, err := h.newReader(t).ListReleaseInventory(context.Background())
	if err != nil {
		t.Fatalf("ListReleaseInventory() error = %v", err)
	}

	assertIdentityObservations(t, "customers", observation.Customers, []stages.IdentityObservation{
		{ID: "cust-a", Name: "Customer A"},
		{ID: "cust-b", Name: "Customer B"},
	})
	assertIdentityObservations(t, "clusters", observation.Clusters, []stages.IdentityObservation{
		{ID: "cluster-a", Name: "Cluster A"},
		{ID: "cluster-b", Name: "Cluster B"},
		{ID: "cluster-c", Name: "Cluster C"},
	})
	if len(observation.Definitions) != 2 ||
		observation.Definitions[0] != (stages.DefinitionObservation{ID: "def-1", Name: "Definition 1"}) ||
		observation.Definitions[1] != (stages.DefinitionObservation{ID: "def-2", Name: "Definition 2"}) {
		t.Fatalf("definitions = %+v, want def-1 and def-2", observation.Definitions)
	}
	wantBundles := []stages.BundleObservation{{
		ID:     "bundle-1",
		Digest: "sha256:abc",
		Status: "BUNDLE_STATUS_VALIDATED",
	}}
	if len(observation.Bundles) != len(wantBundles) || observation.Bundles[0] != wantBundles[0] {
		t.Fatalf("bundles = %+v, want %+v (shared bundles de-duplicated)", observation.Bundles, wantBundles)
	}

	// ClusterRoute exposes no release-definition identity, so the adapter must
	// report the route's own identity and leave DefinitionID empty rather than
	// infer a binding the public API does not publish.
	wantRoutes := []stages.RouteObservation{
		{ID: "route-1", ClusterID: "cluster-a", CustomerID: "cust-a", SourcePrefix: "docker.io/library/"},
		{ID: "route-2", ClusterID: "cluster-b", CustomerID: "cust-b", SourcePrefix: "https://charts.example.com/"},
	}
	if len(observation.Routes) != len(wantRoutes) {
		t.Fatalf("routes = %+v, want %d entries", observation.Routes, len(wantRoutes))
	}
	for index, want := range wantRoutes {
		if observation.Routes[index] != want {
			t.Fatalf("route[%d] = %+v, want %+v", index, observation.Routes[index], want)
		}
	}

	wantRows := []stages.InventoryRow{
		{DefinitionID: "def-1", Revision: 3},
		{DefinitionID: "def-2", Revision: 1},
	}
	if len(observation.Rows) != len(wantRows) {
		t.Fatalf("rows = %+v, want %d entries", observation.Rows, len(wantRows))
	}
	for index, want := range wantRows {
		if observation.Rows[index] != want {
			t.Fatalf("row[%d] = %+v, want %+v", index, observation.Rows[index], want)
		}
	}

	// Every parent-scoped loop, in order, carrying the resolved runner identity.
	wantReads := []readCall{
		{name: "customers", filter: "include_disabled=true"},
		{name: "clusters", filter: "cust-a"},
		{name: "clusters", filter: "cust-b"},
		{name: "definitions", filter: "cust-a"},
		{name: "definitions", filter: "cust-b"},
		{name: "routes", filter: "cluster-a"},
		{name: "routes", filter: "cluster-b"},
		{name: "routes", filter: "cluster-c"},
	}
	assertReadCalls(t, h.orch.readObservations(), wantReads)
	assertReadCalls(t, h.bundle.bundleListObservations(), []readCall{
		{name: "bundles", filter: "def-1"},
		{name: "bundles", filter: "def-2"},
	})
	if calls := h.orch.inventoryCallCount(); calls != 1 {
		t.Fatalf("ListReleaseInventory calls = %d, want 1 unfiltered read", calls)
	}
}

func TestFormalReaderListReleaseInventoryPropagatesParentErrors(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.orch.customers = []*commonv1.Customer{{Id: "cust-a", Name: "Customer A"}}
	h.orch.clustersErr = connect.NewError(connect.CodeInternal, errors.New("cluster store down"))

	_, err := h.newReader(t).ListReleaseInventory(context.Background())
	if err == nil {
		t.Fatal("ListReleaseInventory() error = nil, want the parent read failure")
	}
	if !strings.Contains(err.Error(), "cust-a") {
		t.Fatalf("error = %v, want it to name the failing parent", err)
	}
	// The failure must stop the walk: no definition or route reads follow it.
	for _, call := range h.orch.readObservations() {
		if call.name != "customers" && call.name != "clusters" {
			t.Fatalf("read %s ran after a parent read failed", call.name)
		}
	}
}

func TestFormalReaderGetBundleSendsDefinitionScopedRequest(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	summary := &orchestratorv1.BundleSummary{
		Id:     "bundle-1",
		Digest: &commonv1.ReleaseDigest{Algorithm: "sha256", Value: "sha256:abc"},
		Status: commonv1.BundleStatus_BUNDLE_STATUS_VALIDATED,
	}
	h.bundle.setBundle("def-1", summary, &orchestratorv1.BundleDetail{Summary: summary})

	observation, err := h.newReader(t).GetBundle(context.Background(), stages.BundleRequest{
		BundleID:            "bundle-1",
		ReleaseDefinitionID: "def-1",
	})
	if err != nil {
		t.Fatalf("GetBundle() error = %v", err)
	}
	want := stages.BundleObservation{ID: "bundle-1", Digest: "sha256:abc", Status: "BUNDLE_STATUS_VALIDATED"}
	if observation != want {
		t.Fatalf("GetBundle() = %+v, want %+v", observation, want)
	}

	requests, auth := h.bundle.bundleGetObservations()
	if len(requests) != 1 {
		t.Fatalf("GetBundle calls = %d, want 1", len(requests))
	}
	if requests[0].GetBundleId() != "bundle-1" || requests[0].GetReleaseDefinitionId() != "def-1" {
		t.Fatalf("request = %+v, want bundle-1/def-1", requests[0])
	}
	if len(auth) != 1 || auth[0] != "Bearer e2e-access-token" {
		t.Fatalf("authorization = %v, want the runner bearer token", auth)
	}
}

func TestFormalReaderGetBundleRequiresBothIdentifiers(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	reader := h.newReader(t)
	for _, request := range []stages.BundleRequest{
		{BundleID: "bundle-1"},
		{ReleaseDefinitionID: "def-1"},
		{},
	} {
		if _, err := reader.GetBundle(context.Background(), request); err == nil {
			t.Fatalf("GetBundle(%+v) error = nil, want a fail-closed error", request)
		}
	}
	// The definition-scoped contract is enforced locally, so nothing was sent.
	requests, _ := h.bundle.bundleGetObservations()
	if len(requests) != 0 {
		t.Fatalf("GetBundle sent %d requests for an incomplete request", len(requests))
	}
}

func TestFormalReaderFailsClosedWithoutAuthentication(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// The configured password is a set-but-wrong value: the adapter must refuse
	// to read anything rather than fall through to an unauthenticated request.
	// TestMain publishes the wrong-password variable process-wide because
	// t.Setenv forbids t.Parallel.
	cfg := loadTestConfigWithPasswordEnv(t, h.server.URL, wrongRunnerPasswordEnv)
	clients, err := e2e.NewClientBundleWithHTTPClient(cfg, h.server.Client())
	if err != nil {
		t.Fatalf("NewClientBundleWithHTTPClient() error = %v", err)
	}
	connector, err := NewWithClients(cfg, clients)
	if err != nil {
		t.Fatalf("NewWithClients() error = %v", err)
	}
	reader, err := NewFormalReader(connector)
	if err != nil {
		t.Fatalf("NewFormalReader() error = %v", err)
	}

	if _, err := reader.ListReleaseInventory(context.Background()); !errors.Is(err, e2e.ErrRunnerLogin) {
		t.Fatalf("ListReleaseInventory() error = %v, want errors.Is(..., ErrRunnerLogin)", err)
	}
	if _, err := reader.GetBundle(context.Background(), stages.BundleRequest{BundleID: "b", ReleaseDefinitionID: "d"}); !errors.Is(err, e2e.ErrRunnerLogin) {
		t.Fatalf("GetBundle() error = %v, want errors.Is(..., ErrRunnerLogin)", err)
	}
	if calls := h.orch.readObservations(); len(calls) != 0 {
		t.Fatalf("reads = %+v, want none before authentication", calls)
	}
	if loginCalls := h.auth.loginCount(); loginCalls != 2 {
		t.Fatalf("login attempts = %d, want one per adapter call", loginCalls)
	}
}

// assertIdentityObservations compares identity items in order.
func assertIdentityObservations(t *testing.T, label string, got, want []stages.IdentityObservation) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("%s = %+v, want %+v", label, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s[%d] = %+v, want %+v", label, index, got[index], want[index])
		}
	}
}

// assertReadCalls compares the observed read calls, ignoring the Authorization
// header in the expectation and asserting it separately for every call.
func assertReadCalls(t *testing.T, got, want []readCall) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("reads = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index].name != want[index].name || got[index].filter != want[index].filter {
			t.Fatalf("read[%d] = %+v, want %+v", index, got[index], want[index])
		}
		if got[index].auth != "Bearer e2e-access-token" {
			t.Fatalf("read[%d] %s authorization = %q, want the runner bearer token", index, got[index].name, got[index].auth)
		}
	}
}
