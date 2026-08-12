package main

import (
	"context"
	"errors"
	"fmt"

	"net/http"
	"net/http/httptest"

	"testing"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fake AuthService ---

type fakeAuthHandler struct {
	authv1connect.UnimplementedAuthServiceHandler
	initialized          bool
	deployerKnown        bool // whether the deployer account exists in the system
	loginCallCount       int
	createLocalUserCalls int
	createLocalUserErr   error
	createLocalUserAuth  string // Authorization header observed on CreateLocalUser
}

func (h *fakeAuthHandler) GetInitStatus(context.Context, *connect.Request[authv1.GetInitStatusRequest]) (*connect.Response[authv1.GetInitStatusResponse], error) {
	return connect.NewResponse(&authv1.GetInitStatusResponse{Initialized: h.initialized}), nil
}

func (h *fakeAuthHandler) Initialize(context.Context, *connect.Request[authv1.InitializeRequest]) (*connect.Response[authv1.InitializeResponse], error) {
	h.initialized = true
	return connect.NewResponse(&authv1.InitializeResponse{
		User: &authv1.SessionUser{Id: "admin-1", Username: "admin", Roles: []string{"platform_admin"}},
	}), nil
}

func (h *fakeAuthHandler) Login(_ context.Context, req *connect.Request[authv1.LoginRequest]) (*connect.Response[authv1.LoginResponse], error) {
	h.loginCallCount++
	if req.Msg.GetUsername() == "deployer" && !h.deployerKnown {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
	}
	role := "deployer"
	if req.Msg.GetUsername() == "admin" {
		role = "platform_admin"
	}
	return connect.NewResponse(&authv1.LoginResponse{
		User:         &authv1.SessionUser{Id: req.Msg.GetUsername() + "-1", Username: req.Msg.GetUsername(), Roles: []string{role}, ActiveOrgId: "org-admin"},
		AccessToken:  "token-" + req.Msg.GetUsername(),
		RefreshToken: "refresh-" + req.Msg.GetUsername(),
		TokenType:    "Bearer",
	}), nil
}

func (h *fakeAuthHandler) CreateLocalUser(_ context.Context, req *connect.Request[authv1.CreateLocalUserRequest]) (*connect.Response[authv1.CreateLocalUserResponse], error) {
	h.createLocalUserCalls++
	h.createLocalUserAuth = req.Header().Get("Authorization")
	if h.createLocalUserErr != nil {
		return nil, h.createLocalUserErr
	}
	h.deployerKnown = true
	return connect.NewResponse(&authv1.CreateLocalUserResponse{
		User: &authv1.LocalUser{Id: req.Msg.GetUsername() + "-1", Username: req.Msg.GetUsername(), Roles: req.Msg.GetRoles()},
	}), nil
}
func (h *fakeAuthHandler) GetLocalUser(context.Context, *connect.Request[authv1.GetLocalUserRequest]) (*connect.Response[authv1.GetLocalUserResponse], error) {
	return connect.NewResponse(&authv1.GetLocalUserResponse{
		User: &authv1.LocalUser{Id: "admin-1", Username: "admin", Roles: []string{"platform_admin"}, OrgId: "org-admin", Status: "active"},
	}), nil
}

func newTestAuthServer(t *testing.T, handler *fakeAuthHandler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, authHandler := authv1connect.NewAuthServiceHandler(handler)
	mux.Handle(path, authHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// --- fake BindingService ---

type fakeBindingHandler struct {
	authv1connect.UnimplementedBindingServiceHandler
	createCalls int
	authHeader  string
	createErr   error
}

func (h *fakeBindingHandler) CreateBinding(_ context.Context, req *connect.Request[authv1.CreateBindingRequest]) (*connect.Response[authv1.CreateBindingResponse], error) {
	h.createCalls++
	h.authHeader = req.Header().Get("Authorization")
	if h.createErr != nil {
		return nil, h.createErr
	}
	return connect.NewResponse(&authv1.CreateBindingResponse{Binding: &authv1.OrgCustomerBinding{
		Id: "binding-1", OrgId: req.Msg.GetOrgId(), CustomerId: req.Msg.GetCustomerId(), Status: "active",
	}}), nil
}

func newTestBindingServer(t *testing.T, handler *fakeBindingHandler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, bindingHandler := authv1connect.NewBindingServiceHandler(handler)
	mux.Handle(path, bindingHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// --- fake OrchestratorService ---

type fakeOrchestratorHandler struct {
	orchestratorv1connect.UnimplementedOrchestratorServiceHandler
	revisions      []*commonv1.ValuesRevision
	calls          []string
	authHeaders    []string
	lastIdemKey    string
	createDocument []byte
	submitState    int64
	approveState   int64
	createErr      error
	submitErr      error
	approveErr     error
}

func (h *fakeOrchestratorHandler) ListValuesRevisions(context.Context, *connect.Request[orchestratorv1.ListValuesRevisionsRequest]) (*connect.Response[orchestratorv1.ListValuesRevisionsResponse], error) {
	h.calls = append(h.calls, "List")
	return connect.NewResponse(&orchestratorv1.ListValuesRevisionsResponse{Revisions: h.revisions}), nil
}

func (h *fakeOrchestratorHandler) CreateValuesRevision(_ context.Context, req *connect.Request[orchestratorv1.CreateValuesRevisionRequest]) (*connect.Response[orchestratorv1.CreateValuesRevisionResponse], error) {
	h.calls = append(h.calls, "Create")
	h.authHeaders = append(h.authHeaders, req.Header().Get("Authorization"))
	h.lastIdemKey = req.Header().Get("Idempotency-Key")
	h.createDocument = req.Msg.GetDocument()
	if h.createErr != nil {
		return nil, h.createErr
	}
	return connect.NewResponse(&orchestratorv1.CreateValuesRevisionResponse{Revision: &commonv1.ValuesRevision{
		Id: "revision-new", Status: commonv1.ValuesStatus_VALUES_STATUS_DRAFT, StateVersion: 1, Digest: "sha256:dev-seed",
	}}), nil
}

func (h *fakeOrchestratorHandler) SubmitValuesRevision(_ context.Context, req *connect.Request[orchestratorv1.SubmitValuesRevisionRequest]) (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error) {
	h.calls = append(h.calls, "Submit")
	h.authHeaders = append(h.authHeaders, req.Header().Get("Authorization"))
	if h.submitErr != nil {
		return nil, h.submitErr
	}
	h.submitState = req.Msg.GetExpectedStateVersion() + 1
	return connect.NewResponse(&orchestratorv1.ValuesRevisionDecisionResponse{Revision: &commonv1.ValuesRevision{
		Id: req.Msg.GetRevisionId(), Status: commonv1.ValuesStatus_VALUES_STATUS_PENDING_APPROVAL, StateVersion: h.submitState,
	}}), nil
}

func (h *fakeOrchestratorHandler) ApproveValuesRevision(_ context.Context, req *connect.Request[orchestratorv1.ApproveValuesRevisionRequest]) (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error) {
	h.calls = append(h.calls, "Approve")
	h.authHeaders = append(h.authHeaders, req.Header().Get("Authorization"))
	if h.approveErr != nil {
		return nil, h.approveErr
	}
	h.approveState = req.Msg.GetExpectedStateVersion() + 1
	return connect.NewResponse(&orchestratorv1.ValuesRevisionDecisionResponse{Revision: &commonv1.ValuesRevision{
		Id: req.Msg.GetRevisionId(), Status: commonv1.ValuesStatus_VALUES_STATUS_APPROVED, StateVersion: h.approveState,
	}}), nil
}

func newTestOrchestratorServer(t *testing.T, handler *fakeOrchestratorHandler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, orchHandler := orchestratorv1connect.NewOrchestratorServiceHandler(handler)
	mux.Handle(path, orchHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// --- tests ---

func TestEnsureAuth_InitializesAndLogsInBothActors(t *testing.T) {
	fake := &fakeAuthHandler{deployerKnown: true}
	server := newTestAuthServer(t, fake)
	client := authv1connect.NewAuthServiceClient(http.DefaultClient, server.URL)

	adminToken, deployerToken, adminOrgID, err := ensureAuth(context.Background(), client, "admin", "admin-pass", "deployer", "deployer-pass")
	assert.Equal(t, "org-admin", adminOrgID)
	require.NoError(t, err)
	assert.Equal(t, "token-admin", adminToken)
	assert.Equal(t, "token-deployer", deployerToken)
	assert.Equal(t, 2, fake.loginCallCount)
	assert.True(t, fake.initialized, "Initialize must be called when the system is not initialized")
}
func TestEnsureAuth_SkipsInitializeWhenAlreadyInitialized(t *testing.T) {
	fake := &fakeAuthHandler{initialized: true, deployerKnown: true}
	server := newTestAuthServer(t, fake)
	client := authv1connect.NewAuthServiceClient(http.DefaultClient, server.URL)

	_, _, _, err := ensureAuth(context.Background(), client, "admin", "admin-pass", "deployer", "deployer-pass")
	require.NoError(t, err)
	assert.Equal(t, 2, fake.loginCallCount)
}

func TestEnsureAuth_ProvisionsDeployerViaCreateLocalUser(t *testing.T) {
	fake := &fakeAuthHandler{initialized: true} // deployer unknown
	server := newTestAuthServer(t, fake)
	client := authv1connect.NewAuthServiceClient(http.DefaultClient, server.URL)

	adminToken, deployerToken, adminOrgID, err := ensureAuth(context.Background(), client, "admin", "admin-pass", "deployer", "deployer-pass")
	assert.Equal(t, "org-admin", adminOrgID)
	require.NoError(t, err)
	assert.Equal(t, "token-admin", adminToken)
	assert.Equal(t, "token-deployer", deployerToken)
	assert.Equal(t, 3, fake.loginCallCount, "admin login + failed deployer login + retried deployer login")
	assert.Equal(t, 1, fake.createLocalUserCalls)
	assert.Equal(t, "Bearer token-admin", fake.createLocalUserAuth, "CreateLocalUser must be called with the admin token")
}

func TestEnsureAuth_DeployerCreateLocalUserFailsClosed(t *testing.T) {
	fake := &fakeAuthHandler{
		initialized:        true,
		createLocalUserErr: connect.NewError(connect.CodePermissionDenied, errors.New("requires role platform_admin")),
	}
	server := newTestAuthServer(t, fake)
	client := authv1connect.NewAuthServiceClient(http.DefaultClient, server.URL)

	_, _, _, err := ensureAuth(context.Background(), client, "admin", "admin-pass", "deployer", "deployer-pass")
	require.Error(t, err)
	assert.ErrorContains(t, err, "deployer")
	assert.ErrorContains(t, err, "CreateLocalUser")
	assert.Equal(t, 1, fake.createLocalUserCalls)
}

func TestEnsureAuth_SkipsCreateLocalUserWhenDeployerExists(t *testing.T) {
	fake := &fakeAuthHandler{initialized: true, deployerKnown: true}
	server := newTestAuthServer(t, fake)
	client := authv1connect.NewAuthServiceClient(http.DefaultClient, server.URL)

	_, _, _, err := ensureAuth(context.Background(), client, "admin", "admin-pass", "deployer", "deployer-pass")
	require.NoError(t, err)
	assert.Equal(t, 2, fake.loginCallCount)
	assert.Equal(t, 0, fake.createLocalUserCalls, "existing deployer account must not be re-provisioned")
}
func TestEnsureOrgBinding_CreatesAndIgnoresDuplicate(t *testing.T) {
	fake := &fakeBindingHandler{}
	server := newTestBindingServer(t, fake)
	client := authv1connect.NewBindingServiceClient(http.DefaultClient, server.URL)

	require.NoError(t, ensureOrgBinding(context.Background(), client, "token-admin", "org-1", "customer-1"))
	assert.Equal(t, 1, fake.createCalls)
	assert.Equal(t, "Bearer token-admin", fake.authHeader)

	fake.createErr = connect.NewError(connect.CodeAlreadyExists, errors.New("duplicate_binding"))
	require.NoError(t, ensureOrgBinding(context.Background(), client, "token-admin", "org-1", "customer-1"))
	assert.Equal(t, 2, fake.createCalls, "re-seeding must tolerate an existing active binding")
}

func TestEnsureOrgBinding_ErrorPropagates(t *testing.T) {
	fake := &fakeBindingHandler{createErr: connect.NewError(connect.CodePermissionDenied, errors.New("requires role platform_admin"))}
	server := newTestBindingServer(t, fake)
	client := authv1connect.NewBindingServiceClient(http.DefaultClient, server.URL)

	err := ensureOrgBinding(context.Background(), client, "token-admin", "org-1", "customer-1")
	require.Error(t, err)
	assert.ErrorContains(t, err, "create binding")
}

func TestEnsureValuesRevision_CreatesSubmitsApproves(t *testing.T) {
	fake := &fakeOrchestratorHandler{}
	server := newTestOrchestratorServer(t, fake)
	client := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, server.URL)

	err := ensureValuesRevision(context.Background(), client, "token-deployer", "token-admin", "definition-1", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"List", "Create", "Submit", "Approve"}, fake.calls)
	assert.Equal(t, []string{"Bearer token-deployer", "Bearer token-deployer", "Bearer token-admin"}, fake.authHeaders, "Create/Submit use deployer, Approve uses admin")
	assert.Equal(t, "devseed-create-values-definition-1", fake.lastIdemKey)
	assert.JSONEq(t, `{"environment":"dev-1","replicas":1,"service":"release-manager"}`, string(fake.createDocument))
	assert.Equal(t, int64(3), fake.approveState)
}

func TestEnsureValuesRevision_SkipsWhenAlreadyApproved(t *testing.T) {
	fake := &fakeOrchestratorHandler{revisions: []*commonv1.ValuesRevision{
		{Id: "revision-approved", Status: commonv1.ValuesStatus_VALUES_STATUS_APPROVED},
	}}
	server := newTestOrchestratorServer(t, fake)
	client := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, server.URL)

	err := ensureValuesRevision(context.Background(), client, "token-deployer", "token-admin", "definition-1", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"List"}, fake.calls)
}

func TestEnsureValuesRevision_SubmitsExistingDraft(t *testing.T) {
	fake := &fakeOrchestratorHandler{revisions: []*commonv1.ValuesRevision{
		{Id: "revision-draft", Status: commonv1.ValuesStatus_VALUES_STATUS_DRAFT, StateVersion: 1},
	}}
	server := newTestOrchestratorServer(t, fake)
	client := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, server.URL)

	err := ensureValuesRevision(context.Background(), client, "token-deployer", "token-admin", "definition-1", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"List", "Submit", "Approve"}, fake.calls)
}

func TestEnsureValuesRevision_ApprovesExistingPending(t *testing.T) {
	fake := &fakeOrchestratorHandler{revisions: []*commonv1.ValuesRevision{
		{Id: "revision-pending", Status: commonv1.ValuesStatus_VALUES_STATUS_PENDING_APPROVAL, StateVersion: 2},
	}}
	server := newTestOrchestratorServer(t, fake)
	client := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, server.URL)

	err := ensureValuesRevision(context.Background(), client, "token-deployer", "token-admin", "definition-1", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"List", "Approve"}, fake.calls)
}

func TestEnsureValuesRevision_CreateErrorPropagates(t *testing.T) {
	fake := &fakeOrchestratorHandler{createErr: connect.NewError(connect.CodeInvalidArgument, errors.New("invalid_yaml"))}
	server := newTestOrchestratorServer(t, fake)
	client := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, server.URL)

	err := ensureValuesRevision(context.Background(), client, "token-deployer", "token-admin", "definition-1", 0)
	require.Error(t, err)
	assert.ErrorContains(t, err, "create values revision")
	assert.ErrorContains(t, err, "invalid_yaml")
}

func TestWithAuth_SetsBearerHeader(t *testing.T) {
	req := connect.NewRequest(&orchestratorv1.GetCustomerRequest{CustomerId: "c"})
	withAuth(req, "token-abc")
	assert.Equal(t, "Bearer token-abc", req.Header().Get("Authorization"))
}
