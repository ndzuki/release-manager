package livewire

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"
	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	e2e "github.com/ndzuki/release-manager/test/e2e"
)

const testRunnerPassword = "e2e-test-password"

// TestMain publishes the runner password once. It is set here rather than with
// t.Setenv because t.Setenv forbids t.Parallel, and these tests are independent
// in-process servers that should run concurrently.
func TestMain(m *testing.M) {
	os.Setenv("E2E_RUNNER_PASSWORD", testRunnerPassword)
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

// harness bundles the connector under test with the fakes it talks to.
type harness struct {
	connector *Connector
	auth      *fakeAuth
	orch      *fakeOrchestrator
}

// newHarness wires a real Connector over in-process Connect handlers, so every
// assertion exercises the generated client, the wire encoding, and the auth
// header rather than a hand-rolled stub.
func newHarness(t *testing.T) *harness {
	t.Helper()

	auth := &fakeAuth{}
	orch := &fakeOrchestrator{operations: map[string]*orchestratorv1.Operation{}, emergency: map[string]*orchestratorv1.EmergencyResult{}}

	mux := http.NewServeMux()
	mux.Handle(authv1connect.NewAuthServiceHandler(auth))
	mux.Handle(orchestratorv1connect.NewOrchestratorServiceHandler(orch))
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
	return &harness{connector: connector, auth: auth, orch: orch}
}

// loadTestConfig writes the strict E2E config schema against the in-process
// server, so the adapters are exercised through the same validation path as a
// real run.
func loadTestConfig(t *testing.T, endpoint string) *e2e.Config {
	t.Helper()

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
    password_env: E2E_RUNNER_PASSWORD
k3d:
  kubeconfig: "data/kubeconfig.yaml"
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
    e2e_definition_ids: ["e2e-release-target", "e2e-isolation-target", "e2e-restart-target"]
  e2e_upgrade_targets:
    - definition_id: "e2e-release-target"
      bundle_id: "bundle-1"
      values_revision_id: "values-1"
    - definition_id: "e2e-isolation-target"
      bundle_id: "bundle-1"
      values_revision_id: "values-1"
    - definition_id: "e2e-restart-target"
      bundle_id: "bundle-1"
      values_revision_id: "values-1"
`, endpoint, endpoint, endpoint, endpoint, endpoint, endpoint)

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
