package livewire

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	e2e "github.com/ndzuki/release-manager/test/e2e"
)

const testRunnerPassword = "e2e-test-password"

// wrongRunnerPasswordEnv names a process-wide variable holding a password the
// fake auth service rejects. It exists so the unauthenticated adapter path can
// be tested without t.Setenv, which forbids t.Parallel.
const wrongRunnerPasswordEnv = "LIVEWIRE_WRONG_RUNNER_PASSWORD"

// TestMain publishes the runner password once. It is set here rather than with
// t.Setenv because t.Setenv forbids t.Parallel, and these tests are independent
// in-process servers that should run concurrently.
func TestMain(m *testing.M) {
	os.Setenv("E2E_RUNNER_PASSWORD", testRunnerPassword)
	os.Setenv(wrongRunnerPasswordEnv, "not-the-password")
	os.Exit(m.Run())
}

// fakeAuth is a minimal AuthService that authenticates the runner account.
//
// Handlers run on the Connect server's goroutines while the test goroutine
// asserts on the recorded calls, so every access is mutex-guarded: an
// unsynchronised fake would report races in the test, not in the code under
// test.
type fakeAuth struct {
	authv1connect.UnimplementedAuthServiceHandler

	mu      sync.Mutex
	logins  int
	lastReq *authv1.LoginRequest
}

func (f *fakeAuth) Login(_ context.Context, req *connect.Request[authv1.LoginRequest]) (*connect.Response[authv1.LoginResponse], error) {
	f.mu.Lock()
	f.logins++
	f.lastReq = req.Msg
	f.mu.Unlock()
	if req.Msg.GetPassword() != testRunnerPassword {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("bad password"))
	}
	return connect.NewResponse(&authv1.LoginResponse{
		AccessToken: "e2e-access-token",
		User:        &authv1.SessionUser{Id: "e2e-runner-id"},
	}), nil
}

func (f *fakeAuth) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

// fakeOrchestrator records the calls the adapters make and serves canned
// responses.
type fakeOrchestrator struct {
	orchestratorv1connect.UnimplementedOrchestratorServiceHandler

	mu sync.Mutex

	inventoryRows  []*orchestratorv1.ReleaseInventoryRow
	inventoryErr   error
	inventoryCalls int

	createResponse *orchestratorv1.CreateOperationResponse
	createErr      error
	creates        []*orchestratorv1.CreateOperationRequest
	createKeys     []string

	rollbackResponse *orchestratorv1.RollbackReleaseResponse
	rollbackErr      error
	rollbacks        []*orchestratorv1.RollbackReleaseRequest
	rollbackKeys     []string

	// operations is the fallback lookup; getSequence takes precedence and lets a
	// test script a non-terminal-then-terminal progression without racing on a
	// map from the test goroutine.
	operations  map[string]*orchestratorv1.Operation
	getSequence []*orchestratorv1.Operation
	emergency   map[string]*orchestratorv1.EmergencyResult
	getCalls    int
	getErr      error

	cancels   []string
	cancelErr error

	emergencyTargets []*orchestratorv1.EmergencyTarget
	targetsErr       error
	emergencyResp    *orchestratorv1.ExecuteEmergencyChangeResponse
	emergencyErr     error
	emergencies      []*orchestratorv1.ExecuteEmergencyChangeRequest

	// read-only inventory sources. The per-parent maps model the parent-scoped
	// RPCs the reader must loop over; reads records each call so a test can
	// assert the exact filters and identity that were sent.
	customers             []*commonv1.Customer
	customersErr          error
	clustersByCustomer    map[string][]*commonv1.Cluster
	clustersErr           error
	definitionsByCustomer map[string][]*commonv1.ReleaseDefinition
	definitionsErr        error
	routesByCluster       map[string][]*orchestratorv1.ClusterRoute
	routesErr             error
	reads                 []readCall
}

// readCall records one read RPC: which route was called, the filter it carried,
// and the Authorization header the adapter attached.
type readCall struct {
	name   string
	filter string
	auth   string
}

func (f *fakeOrchestrator) ListReleaseInventory(context.Context, *connect.Request[orchestratorv1.ListReleaseInventoryRequest]) (*connect.Response[orchestratorv1.ListReleaseInventoryResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inventoryCalls++
	if f.inventoryErr != nil {
		return nil, f.inventoryErr
	}
	return connect.NewResponse(&orchestratorv1.ListReleaseInventoryResponse{Rows: f.inventoryRows}), nil
}

func (f *fakeOrchestrator) CreateOperation(_ context.Context, req *connect.Request[orchestratorv1.CreateOperationRequest]) (*connect.Response[orchestratorv1.CreateOperationResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, req.Msg)
	f.createKeys = append(f.createKeys, req.Header().Get("Idempotency-Key"))
	if f.createErr != nil {
		return nil, f.createErr
	}
	response := f.createResponse
	if response == nil {
		response = &orchestratorv1.CreateOperationResponse{OperationId: "op-created", State: "pending"}
	}
	return connect.NewResponse(response), nil
}

func (f *fakeOrchestrator) RollbackRelease(_ context.Context, req *connect.Request[orchestratorv1.RollbackReleaseRequest]) (*connect.Response[orchestratorv1.RollbackReleaseResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbacks = append(f.rollbacks, req.Msg)
	f.rollbackKeys = append(f.rollbackKeys, req.Header().Get("Idempotency-Key"))
	if f.rollbackErr != nil {
		return nil, f.rollbackErr
	}
	response := f.rollbackResponse
	if response == nil {
		response = &orchestratorv1.RollbackReleaseResponse{OperationId: "op-rollback", State: "pending"}
	}
	return connect.NewResponse(response), nil
}

func (f *fakeOrchestrator) GetOperation(_ context.Context, req *connect.Request[orchestratorv1.GetOperationRequest]) (*connect.Response[orchestratorv1.GetOperationResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if len(f.getSequence) > 0 {
		index := f.getCalls - 1
		if index >= len(f.getSequence) {
			index = len(f.getSequence) - 1
		}
		operation := f.getSequence[index]
		return connect.NewResponse(&orchestratorv1.GetOperationResponse{
			Operation:       operation,
			EmergencyResult: f.emergency[operation.GetOperationId()],
		}), nil
	}
	operation, ok := f.operations[req.Msg.GetOperationId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("operation not found"))
	}
	return connect.NewResponse(&orchestratorv1.GetOperationResponse{
		Operation:       operation,
		EmergencyResult: f.emergency[req.Msg.GetOperationId()],
	}), nil
}

func (f *fakeOrchestrator) CancelOperation(_ context.Context, req *connect.Request[orchestratorv1.CancelOperationRequest]) (*connect.Response[orchestratorv1.CancelOperationResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, req.Msg.GetOperationId())
	if f.cancelErr != nil {
		return nil, f.cancelErr
	}
	return connect.NewResponse(&orchestratorv1.CancelOperationResponse{}), nil
}

func (f *fakeOrchestrator) ListEmergencyTargets(context.Context, *connect.Request[orchestratorv1.ListEmergencyTargetsRequest]) (*connect.Response[orchestratorv1.ListEmergencyTargetsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.targetsErr != nil {
		return nil, f.targetsErr
	}
	return connect.NewResponse(&orchestratorv1.ListEmergencyTargetsResponse{Targets: f.emergencyTargets}), nil
}

func (f *fakeOrchestrator) ExecuteEmergencyChange(_ context.Context, req *connect.Request[orchestratorv1.ExecuteEmergencyChangeRequest]) (*connect.Response[orchestratorv1.ExecuteEmergencyChangeResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emergencies = append(f.emergencies, req.Msg)
	if f.emergencyErr != nil {
		return nil, f.emergencyErr
	}
	response := f.emergencyResp
	if response == nil {
		response = &orchestratorv1.ExecuteEmergencyChangeResponse{
			OperationId: "op-emergency",
			Result: &orchestratorv1.EmergencyResult{
				Requested:         true,
				ConvergencePolicy: orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REVERT_ON_NEXT_RECONCILE,
			},
		}
	}
	return connect.NewResponse(response), nil
}

// --- read-only routes -------------------------------------------------------

func (f *fakeOrchestrator) ListCustomers(_ context.Context, req *connect.Request[orchestratorv1.ListCustomersRequest]) (*connect.Response[orchestratorv1.ListCustomersResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordRead("customers", fmt.Sprintf("include_disabled=%t", req.Msg.GetIncludeDisabled()), req)
	if f.customersErr != nil {
		return nil, f.customersErr
	}
	return connect.NewResponse(&orchestratorv1.ListCustomersResponse{Customers: f.customers}), nil
}

func (f *fakeOrchestrator) ListClusters(_ context.Context, req *connect.Request[orchestratorv1.ListClustersRequest]) (*connect.Response[orchestratorv1.ListClustersResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordRead("clusters", req.Msg.GetCustomerId(), req)
	if f.clustersErr != nil {
		return nil, f.clustersErr
	}
	return connect.NewResponse(&orchestratorv1.ListClustersResponse{
		Clusters: f.clustersByCustomer[req.Msg.GetCustomerId()],
	}), nil
}

func (f *fakeOrchestrator) ListReleaseDefinitions(_ context.Context, req *connect.Request[orchestratorv1.ListReleaseDefinitionsRequest]) (*connect.Response[orchestratorv1.ListReleaseDefinitionsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordRead("definitions", req.Msg.GetCustomerId(), req)
	if f.definitionsErr != nil {
		return nil, f.definitionsErr
	}
	return connect.NewResponse(&orchestratorv1.ListReleaseDefinitionsResponse{
		Definitions: f.definitionsByCustomer[req.Msg.GetCustomerId()],
	}), nil
}

func (f *fakeOrchestrator) GetClusterRoutes(_ context.Context, req *connect.Request[orchestratorv1.GetClusterRoutesRequest]) (*connect.Response[orchestratorv1.GetClusterRoutesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordRead("routes", req.Msg.GetClusterId(), req)
	if f.routesErr != nil {
		return nil, f.routesErr
	}
	return connect.NewResponse(&orchestratorv1.GetClusterRoutesResponse{
		Routes: f.routesByCluster[req.Msg.GetClusterId()],
	}), nil
}

// recordRead appends one observation. The caller holds f.mu.
func (f *fakeOrchestrator) recordRead(name, filter string, req interface{ Header() http.Header }) {
	f.reads = append(f.reads, readCall{name: name, filter: filter, auth: req.Header().Get("Authorization")})
}

// readObservations returns a copy of the recorded read calls.
func (f *fakeOrchestrator) readObservations() []readCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]readCall(nil), f.reads...)
}

// setInventorySources installs the per-parent read sources.
func (f *fakeOrchestrator) setInventorySources(
	customers []*commonv1.Customer,
	clusters map[string][]*commonv1.Cluster,
	definitions map[string][]*commonv1.ReleaseDefinition,
	routes map[string][]*orchestratorv1.ClusterRoute,
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.customers = customers
	f.clustersByCustomer = clusters
	f.definitionsByCustomer = definitions
	f.routesByCluster = routes
}

// --- synchronized test accessors -------------------------------------------

func (f *fakeOrchestrator) setInventory(rows ...*orchestratorv1.ReleaseInventoryRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inventoryRows = rows
}

func (f *fakeOrchestrator) inventoryCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inventoryCalls
}

func (f *fakeOrchestrator) setOperations(operations map[string]*orchestratorv1.Operation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.operations = operations
}

func (f *fakeOrchestrator) setGetSequence(sequence ...*orchestratorv1.Operation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getSequence = sequence
}

func (f *fakeOrchestrator) setEmergencyResults(results map[string]*orchestratorv1.EmergencyResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emergency = results
}

func (f *fakeOrchestrator) setCreateError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createErr = err
}

func (f *fakeOrchestrator) setEmergencyTargets(targets ...*orchestratorv1.EmergencyTarget) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emergencyTargets = targets
}

func (f *fakeOrchestrator) setEmergencyResponse(response *orchestratorv1.ExecuteEmergencyChangeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emergencyResp = response
}

func (f *fakeOrchestrator) setRollbackResponse(response *orchestratorv1.RollbackReleaseResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbackResponse = response
}

func (f *fakeOrchestrator) createRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates)
}

func (f *fakeOrchestrator) createRequest(index int) *orchestratorv1.CreateOperationRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates[index]
}

func (f *fakeOrchestrator) createKey(index int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createKeys[index]
}

func (f *fakeOrchestrator) rollbackRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rollbacks)
}

func (f *fakeOrchestrator) rollbackRequest(index int) *orchestratorv1.RollbackReleaseRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rollbacks[index]
}

func (f *fakeOrchestrator) rollbackKey(index int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rollbackKeys[index]
}

func (f *fakeOrchestrator) cancelRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cancels...)
}

func (f *fakeOrchestrator) emergencyRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.emergencies)
}

func (f *fakeOrchestrator) emergencyRequest(index int) *orchestratorv1.ExecuteEmergencyChangeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.emergencies[index]
}

// lastEmergencyReplicas returns the replica count of the most recent emergency
// write, or the supplied fallback when no write has happened yet.
func (f *fakeOrchestrator) lastEmergencyReplicas(fallback int32) int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.emergencies) == 0 {
		return fallback
	}
	return f.emergencies[len(f.emergencies)-1].GetSetReplicas()
}

// fakeBundle serves the definition-scoped bundle routes.
type fakeBundle struct {
	orchestratorv1connect.UnimplementedBundleServiceHandler

	mu sync.Mutex

	bundlesByDefinition map[string][]*orchestratorv1.BundleSummary
	listErr             error
	listCalls           []readCall

	details  map[string]*orchestratorv1.BundleDetail
	getErr   error
	getCalls []*orchestratorv1.GetBundleRequest
	getAuth  []string
}

func (f *fakeBundle) ListBundles(_ context.Context, req *connect.Request[orchestratorv1.ListBundlesRequest]) (*connect.Response[orchestratorv1.ListBundlesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls = append(f.listCalls, readCall{
		name:   "bundles",
		filter: req.Msg.GetReleaseDefinitionId(),
		auth:   req.Header().Get("Authorization"),
	})
	if f.listErr != nil {
		return nil, f.listErr
	}
	return connect.NewResponse(&orchestratorv1.ListBundlesResponse{
		Bundles: f.bundlesByDefinition[req.Msg.GetReleaseDefinitionId()],
	}), nil
}

func (f *fakeBundle) GetBundle(_ context.Context, req *connect.Request[orchestratorv1.GetBundleRequest]) (*connect.Response[orchestratorv1.GetBundleResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls = append(f.getCalls, req.Msg)
	f.getAuth = append(f.getAuth, req.Header().Get("Authorization"))
	if f.getErr != nil {
		return nil, f.getErr
	}
	detail, ok := f.details[bundleKey(req.Msg.GetReleaseDefinitionId(), req.Msg.GetBundleId())]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("bundle not found"))
	}
	return connect.NewResponse(&orchestratorv1.GetBundleResponse{Bundle: detail}), nil
}

// bundleKey scopes a bundle to its definition, because GetBundle is a
// definition-scoped read.
func bundleKey(definitionID, bundleID string) string {
	return definitionID + "|" + bundleID
}

func (f *fakeBundle) bundleListObservations() []readCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]readCall(nil), f.listCalls...)
}

func (f *fakeBundle) bundleGetObservations() (requests []*orchestratorv1.GetBundleRequest, auth []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*orchestratorv1.GetBundleRequest(nil), f.getCalls...), append([]string(nil), f.getAuth...)
}

// setBundle installs one definition-scoped bundle and its detail.
func (f *fakeBundle) setBundle(definitionID string, summary *orchestratorv1.BundleSummary, detail *orchestratorv1.BundleDetail) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bundlesByDefinition == nil {
		f.bundlesByDefinition = map[string][]*orchestratorv1.BundleSummary{}
	}
	f.bundlesByDefinition[definitionID] = append(f.bundlesByDefinition[definitionID], summary)
	if f.details == nil {
		f.details = map[string]*orchestratorv1.BundleDetail{}
	}
	f.details[bundleKey(definitionID, summary.GetId())] = detail
}

// fakeOperator serves the operator gateway's session route so the control-plane
// barrier can be exercised through the real generated client.
type fakeOperator struct {
	operatorv1connect.UnimplementedOperatorServiceHandler

	mu       sync.Mutex
	sessions []*operatorv1.OperatorSession
	err      error
	calls    int
	// requested records the operator id of every session read, so tests can
	// assert the runner addresses the operator the seed published rather than
	// sending a blank request the API rejects.
	requested []string
}

func (f *fakeOperator) GetActiveOperatorSession(_ context.Context, req *connect.Request[operatorv1.GetActiveOperatorSessionRequest]) (*connect.Response[operatorv1.GetActiveOperatorSessionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.requested = append(f.requested, req.Msg.GetOperatorId())
	if f.err != nil {
		return nil, f.err
	}
	if len(f.sessions) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no active session"))
	}
	// Serve the scripted progression, holding the last state once exhausted.
	index := f.calls - 1
	if index >= len(f.sessions) {
		index = len(f.sessions) - 1
	}
	return connect.NewResponse(&operatorv1.GetActiveOperatorSessionResponse{Session: f.sessions[index]}), nil
}

// requestedOperatorIDs returns the operator id of every session read so far.
func (f *fakeOperator) requestedOperatorIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requested...)
}

func (f *fakeOperator) setSessions(sessions ...*operatorv1.OperatorSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = sessions
	f.calls = 0
}

func (f *fakeOperator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// setError makes the session route fail, which is what an operator gateway
// without an established control stream answers.
func (f *fakeOperator) setError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// harness bundles the connector under test with the fakes it talks to.
type harness struct {
	connector *Connector
	auth      *fakeAuth
	orch      *fakeOrchestrator
	operator  *fakeOperator
	bundle    *fakeBundle

	cfg     *e2e.Config
	clients *e2e.ClientBundle
	server  *httptest.Server

	healthCode atomic.Int32
	readyCode  atomic.Int32
	envStatus  atomic.Int32
	envBody    atomic.Value
}

// newHarness wires a real Connector over in-process Connect handlers, so every
// assertion exercises the generated client, the wire encoding, and the auth
// header rather than a hand-rolled stub.
func newHarness(t *testing.T) *harness {
	t.Helper()

	auth := &fakeAuth{}
	orch := &fakeOrchestrator{operations: map[string]*orchestratorv1.Operation{}, emergency: map[string]*orchestratorv1.EmergencyResult{}}
	operator := &fakeOperator{}
	bundle := &fakeBundle{}

	h := &harness{connector: nil, auth: auth, orch: orch, operator: operator, bundle: bundle}
	h.healthCode.Store(http.StatusOK)
	h.readyCode.Store(http.StatusOK)
	h.envStatus.Store(http.StatusOK)
	h.envBody.Store(defaultEnvironmentBody)

	mux := http.NewServeMux()
	mux.Handle(authv1connect.NewAuthServiceHandler(auth))
	mux.Handle(orchestratorv1connect.NewOrchestratorServiceHandler(orch))
	mux.Handle(orchestratorv1connect.NewBundleServiceHandler(bundle))
	mux.Handle(operatorv1connect.NewOperatorServiceHandler(operator))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(h.healthCode.Load()))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(h.readyCode.Load()))
	})
	mux.HandleFunc("/environment", func(w http.ResponseWriter, _ *http.Request) {
		status := int(h.envStatus.Load())
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		body, ok := h.envBody.Load().(string)
		if !ok {
			body = defaultEnvironmentBody
		}
		written, writeErr := fmt.Fprint(w, body)
		if writeErr != nil {
			t.Logf("write environment body: wrote %d of %d bytes: %v", written, len(body), writeErr)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cfg := loadTestConfig(t, server.URL)
	clients, err := e2e.NewClientBundleWithHTTPClient(cfg, server.Client())
	if err != nil {
		t.Fatalf("NewClientBundleWithHTTPClient() error = %v", err)
	}
	connector, err := NewWithClients(cfg, clients)
	if err != nil {
		t.Fatalf("NewWithClients() error = %v", err)
	}
	h.connector = connector
	h.cfg = cfg
	h.clients = clients
	h.server = server
	return h
}

// defaultEnvironmentBody is the /environment payload every healthy fake
// endpoint answers. It mirrors the four-field contract of REQ-065.
const defaultEnvironmentBody = `{"service":"release-orchestrator","environment":"test","environment_id":"livewire-unit","production":false}`

// setHealth controls the /health status served to the control-plane barrier.
func (h *harness) setHealth(code int) { h.healthCode.Store(int32(code)) }

// setReady controls the /readyz status served to the control-plane observer.
func (h *harness) setReady(code int) { h.readyCode.Store(int32(code)) }

// setEnvironment controls the /environment status and body.
func (h *harness) setEnvironment(status int, body string) {
	h.envStatus.Store(int32(status))
	if body != "" {
		h.envBody.Store(body)
	}
}

// newObserver builds the control-plane observer over the harness server.
func (h *harness) newObserver(t *testing.T) *ControlPlaneObserver {
	t.Helper()
	observer, err := NewControlPlaneObserver(h.cfg, h.connector)
	if err != nil {
		t.Fatalf("NewControlPlaneObserver() error = %v", err)
	}
	return observer.WithHTTPClient(h.server.Client())
}

// newReader builds the read-only inventory/bundle reader over the harness
// connector.
func (h *harness) newReader(t *testing.T) *FormalReader {
	t.Helper()
	reader, err := NewFormalReader(h.connector)
	if err != nil {
		t.Fatalf("NewFormalReader() error = %v", err)
	}
	return reader
}

// newWaiter builds the control-plane barrier over the harness server.
func (h *harness) newWaiter(t *testing.T) *ControlPlaneWaiter {
	t.Helper()
	session, err := e2e.NewRunnerSession(h.cfg, h.clients)
	if err != nil {
		t.Fatalf("NewRunnerSession() error = %v", err)
	}
	// Authenticate before handing the session to a waiter, on a budget of its
	// own. EnsureLogin is cached and no-ops once a token exists, so tests that
	// pass a deliberately tiny deadline — the barrier must honour the caller's
	// context — exercise the poll loop instead of racing the login against that
	// deadline. Under CPU pressure the login alone spent the budget and the
	// barrier then reported a login error where the caller's deadline was due.
	loginCtx, cancelLogin := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelLogin()
	if err := session.EnsureLogin(loginCtx); err != nil {
		t.Fatalf("EnsureLogin() error = %v", err)
	}
	waiter, err := NewControlPlaneWaiter(h.cfg, h.clients, session)
	if err != nil {
		t.Fatalf("NewControlPlaneWaiter() error = %v", err)
	}
	return waiter.WithHTTPClient(h.server.Client()).WithPollInterval(time.Millisecond)
}

// loadTestConfig writes the strict E2E config schema against the in-process
// server, so the adapters are exercised through the same validation path as a
// real run.
func loadTestConfig(t *testing.T, endpoint string) *e2e.Config {
	t.Helper()
	return loadTestConfigWithPasswordEnv(t, endpoint, "E2E_RUNNER_PASSWORD")
}

// loadTestConfigWithPasswordEnv is loadTestConfig with an explicit password
// environment variable, so a test can exercise the unauthenticated path without
// t.Setenv (which forbids t.Parallel).
func loadTestConfigWithPasswordEnv(t *testing.T, endpoint, passwordEnv string) *e2e.Config {
	t.Helper()

	kubeconfigPath := writeTestKubeconfig(t)
	body := fmt.Sprintf(`environment: test
environment_id: livewire-unit
endpoints:
  release_orchestrator: %q
  release_webhook: %q
  release_operator: %q
  release_auth: %q
  release_notifier: %q
  release_api: %q
credentials:
  e2e_runner:
    username: e2e-runner
    password_env: %q
k3d:
  kubeconfig: %q
  context: "livewire-test"
  cluster_contexts:
    dev-customer-a-direct: "k3d-dev-customer-a-direct"
  test_namespace: "release-manager-dev"
  restart_targets:
    namespace: "release-manager-dev"
    deployments: ["auth", "operator-gateway", "orchestrator"]
seed:
  customers: ["dev-customer-a"]
  clusters_per_customer: 1
  fixture_version: "v1"
  expected_identity:
    customers: 1
    clusters: 1
    routes_basic: 1
    definitions_basic: 1
    bundles: 1
    e2e_definition_ids: ["11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444"]
  e2e_upgrade_targets:
    - logical_key: "e2e-release-target"
      definition_id: "11111111-1111-1111-1111-111111111111"
      bundle_id: "bundle-1"
      values_revision_id: "values-1"
    - logical_key: "e2e-isolation-target"
      definition_id: "22222222-2222-2222-2222-222222222222"
      bundle_id: "bundle-1"
      values_revision_id: "values-1"
    - logical_key: "e2e-restart-target"
      definition_id: "44444444-4444-4444-4444-444444444444"
      bundle_id: "bundle-1"
      values_revision_id: "values-1"
  e2e_emergency_definition_id: "33333333-3333-3333-3333-333333333333"
  e2e_operator_id: "operator-1"
`, endpoint, endpoint, endpoint, endpoint, endpoint, endpoint, passwordEnv, kubeconfigPath)

	path := filepath.Join(t.TempDir(), "e2e-env-config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := e2e.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	return cfg
}

// writeTestKubeconfig writes a syntactically valid kubeconfig so the Specs
// tests can build the typed clientset without a cluster: client-go parses the
// config eagerly and connects lazily, so no API server is needed.
func writeTestKubeconfig(t *testing.T) string {
	t.Helper()

	const body = `apiVersion: v1
kind: Config
clusters:
  - name: livewire-test
    cluster:
      server: https://127.0.0.1:6443
contexts:
  - name: livewire-test
    context:
      cluster: livewire-test
      user: livewire-test
current-context: livewire-test
users:
  - name: livewire-test
    user:
      token: livewire-test-token
`
	path := filepath.Join(t.TempDir(), "kubeconfig.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// inventoryRow builds one inventory row for a definition.
func inventoryRow(definitionID string, revision int32, active *orchestratorv1.ActiveOperationRef) *orchestratorv1.ReleaseInventoryRow {
	return &orchestratorv1.ReleaseInventoryRow{
		ReleaseDefinitionId: definitionID,
		Revision:            revision,
		ActiveOperation:     active,
	}
}

// succeededOperation builds a terminal UPGRADE operation.
func succeededOperation(id, definitionID string, revision int32) *orchestratorv1.Operation {
	return &orchestratorv1.Operation{
		OperationId:         id,
		ReleaseDefinitionId: definitionID,
		OperationType:       "UPGRADE",
		State:               orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED,
		TargetRevision:      revision,
	}
}

// runningOperation builds a non-terminal UPGRADE operation.
func runningOperation(id, definitionID string) *orchestratorv1.Operation {
	return &orchestratorv1.Operation{
		OperationId:         id,
		ReleaseDefinitionId: definitionID,
		OperationType:       "UPGRADE",
		State:               orchestratorv1.OperationStatus_OPERATION_STATUS_RUNNING,
	}
}
