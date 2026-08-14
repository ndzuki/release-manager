package devfixture

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	trustv1 "github.com/ndzuki/release-manager/api/gen/trust/v1"
	webhookv1 "github.com/ndzuki/release-manager/api/gen/webhook/v1"
)

// fakeAuth is an in-memory AuthService: init once, local users with
// passwords, deterministic tokens.
type fakeAuth struct {
	mu          sync.Mutex
	initialized bool
	users       map[string]*authv1.LocalUser
	passwords   map[string]string
	orgID       string
	loginCalls  int
	createCalls int
	initCalls   int
}

func newFakeAuth() *fakeAuth {
	return &fakeAuth{
		users:     map[string]*authv1.LocalUser{},
		passwords: map[string]string{},
		orgID:     "org-dev-platform",
	}
}

func (f *fakeAuth) GetInitStatus(context.Context, *connect.Request[authv1.GetInitStatusRequest]) (*connect.Response[authv1.GetInitStatusResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&authv1.GetInitStatusResponse{Initialized: f.initialized}), nil
}

func (f *fakeAuth) Initialize(_ context.Context, req *connect.Request[authv1.InitializeRequest]) (*connect.Response[authv1.InitializeResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initCalls++
	if f.initialized {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("already initialized"))
	}
	f.initialized = true
	f.users[req.Msg.GetUsername()] = &authv1.LocalUser{
		Id: "user-" + req.Msg.GetUsername(), Username: req.Msg.GetUsername(),
		Roles: []string{"platform_admin"}, OrgId: f.orgID, Status: "active",
	}
	f.passwords[req.Msg.GetUsername()] = req.Msg.GetPassword()
	return connect.NewResponse(&authv1.InitializeResponse{AccessToken: "token-" + req.Msg.GetUsername()}), nil
}

func (f *fakeAuth) Login(_ context.Context, req *connect.Request[authv1.LoginRequest]) (*connect.Response[authv1.LoginResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginCalls++
	user, ok := f.users[req.Msg.GetUsername()]
	if !ok || f.passwords[req.Msg.GetUsername()] != req.Msg.GetPassword() {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid credentials"))
	}
	return connect.NewResponse(&authv1.LoginResponse{
		User:        &authv1.SessionUser{Id: user.GetId(), Username: user.GetUsername(), Roles: user.GetRoles(), ActiveOrgId: user.GetOrgId()},
		AccessToken: "token-" + user.GetUsername(),
	}), nil
}

func (f *fakeAuth) CreateLocalUser(_ context.Context, req *connect.Request[authv1.CreateLocalUserRequest]) (*connect.Response[authv1.CreateLocalUserResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if _, ok := f.users[req.Msg.GetUsername()]; ok {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("user already exists"))
	}
	roles := req.Msg.GetRoles()
	if len(roles) == 0 {
		roles = []string{"viewer"}
	}
	orgID := req.Msg.GetOrgId()
	if orgID == "" {
		orgID = f.orgID
	}
	user := &authv1.LocalUser{
		Id: "user-" + req.Msg.GetUsername(), Username: req.Msg.GetUsername(),
		Roles: roles, OrgId: orgID, Status: "active",
	}
	f.users[req.Msg.GetUsername()] = user
	f.passwords[req.Msg.GetUsername()] = req.Msg.GetPassword()
	return connect.NewResponse(&authv1.CreateLocalUserResponse{User: user}), nil
}

func (f *fakeAuth) GetLocalUser(_ context.Context, req *connect.Request[authv1.GetLocalUserRequest]) (*connect.Response[authv1.GetLocalUserResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user, ok := f.users[req.Msg.GetUsername()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
	}
	return connect.NewResponse(&authv1.GetLocalUserResponse{User: user}), nil
}

// fakeBinding is an in-memory BindingService.
type fakeBinding struct {
	mu       sync.Mutex
	bindings map[string]*authv1.OrgCustomerBinding // key: customer id
}

func newFakeBinding() *fakeBinding {
	return &fakeBinding{bindings: map[string]*authv1.OrgCustomerBinding{}}
}

func (f *fakeBinding) ListBindings(_ context.Context, _ *connect.Request[authv1.ListBindingsRequest]) (*connect.Response[authv1.ListBindingsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*authv1.OrgCustomerBinding, 0, len(f.bindings))
	for _, binding := range f.bindings {
		out = append(out, binding)
	}
	return connect.NewResponse(&authv1.ListBindingsResponse{Bindings: out}), nil
}

func (f *fakeBinding) CreateBinding(_ context.Context, req *connect.Request[authv1.CreateBindingRequest]) (*connect.Response[authv1.CreateBindingResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.bindings[req.Msg.GetCustomerId()]; ok {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("binding exists"))
	}
	binding := &authv1.OrgCustomerBinding{
		Id: "binding-" + req.Msg.GetCustomerId(), OrgId: req.Msg.GetOrgId(),
		CustomerId: req.Msg.GetCustomerId(), Status: "active",
	}
	f.bindings[req.Msg.GetCustomerId()] = binding
	return connect.NewResponse(&authv1.CreateBindingResponse{Binding: binding}), nil
}

// fakeOperation is one in-flight operation in the fake orchestrator. Each
// GetOperation advances the state one step toward the configured terminal.
type fakeOperation struct {
	id       string
	key      string
	state    orchestratorv1.OperationStatus
	terminal orchestratorv1.OperationStatus
}

var opProgression = []orchestratorv1.OperationStatus{
	orchestratorv1.OperationStatus_OPERATION_STATUS_PENDING,
	orchestratorv1.OperationStatus_OPERATION_STATUS_PREFLIGHT,
	orchestratorv1.OperationStatus_OPERATION_STATUS_QUEUED,
	orchestratorv1.OperationStatus_OPERATION_STATUS_RUNNING,
}

func (op *fakeOperation) advance() {
	if op.state == op.terminal {
		return
	}
	for i, state := range opProgression {
		if state == op.state {
			if i+1 < len(opProgression) {
				op.state = opProgression[i+1]
			} else {
				op.state = op.terminal
			}
			return
		}
	}
	op.state = op.terminal
}

// fakeOrchestrator is an in-memory OrchestratorService covering exactly the
// seed's methods, with per-method call counters and programmable failures.
type fakeOrchestrator struct {
	mu          sync.Mutex
	customers   map[string]*commonv1.Customer
	clusters    map[string]*commonv1.Cluster
	routes      map[string]*orchestratorv1.ClusterRoute
	definitions map[string]*commonv1.ReleaseDefinition
	values      map[string]*commonv1.ValuesRevision
	tokens      map[string]string // cluster id → token
	operations  map[string]*fakeOperation

	counters map[string]int
	// opKeyTerminal overrides the terminal state for operations created
	// with a matching idempotency key (default succeeded).
	opKeyTerminal map[string]orchestratorv1.OperationStatus

	nextOpID int
}

func newFakeOrchestrator() *fakeOrchestrator {
	return &fakeOrchestrator{
		customers:     map[string]*commonv1.Customer{},
		clusters:      map[string]*commonv1.Cluster{},
		routes:        map[string]*orchestratorv1.ClusterRoute{},
		definitions:   map[string]*commonv1.ReleaseDefinition{},
		values:        map[string]*commonv1.ValuesRevision{},
		tokens:        map[string]string{},
		operations:    map[string]*fakeOperation{},
		counters:      map[string]int{},
		opKeyTerminal: map[string]orchestratorv1.OperationStatus{},
	}
}

func (f *fakeOrchestrator) bump(name string) int {
	f.counters[name]++
	return f.counters[name]
}

func (f *fakeOrchestrator) count(name string) int { return f.counters[name] }

func (f *fakeOrchestrator) ListCustomers(_ context.Context, _ *connect.Request[orchestratorv1.ListCustomersRequest]) (*connect.Response[orchestratorv1.ListCustomersResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("ListCustomers")
	out := make([]*commonv1.Customer, 0, len(f.customers))
	for _, customer := range f.customers {
		out = append(out, customer)
	}
	return connect.NewResponse(&orchestratorv1.ListCustomersResponse{Customers: out}), nil
}

func (f *fakeOrchestrator) CreateCustomer(_ context.Context, req *connect.Request[orchestratorv1.CreateCustomerRequest]) (*connect.Response[orchestratorv1.CreateCustomerResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("CreateCustomer")
	msg := req.Msg
	if _, ok := f.customers[msg.GetId()]; ok {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("customer exists"))
	}
	f.customers[msg.GetId()] = &commonv1.Customer{Id: msg.GetId(), Name: msg.GetName(), Slug: msg.GetSlug(), Status: "active"}
	return connect.NewResponse(&orchestratorv1.CreateCustomerResponse{Customer: f.customers[msg.GetId()]}), nil
}

func (f *fakeOrchestrator) GetCluster(_ context.Context, req *connect.Request[orchestratorv1.GetClusterRequest]) (*connect.Response[orchestratorv1.GetClusterResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("GetCluster")
	cluster, ok := f.clusters[req.Msg.GetClusterId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("cluster not found"))
	}
	return connect.NewResponse(&orchestratorv1.GetClusterResponse{Cluster: cluster}), nil
}

func (f *fakeOrchestrator) CreateCluster(_ context.Context, req *connect.Request[orchestratorv1.CreateClusterRequest]) (*connect.Response[orchestratorv1.CreateClusterResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("CreateCluster")
	msg := req.Msg
	if _, ok := f.clusters[msg.GetId()]; ok {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("cluster exists"))
	}
	f.clusters[msg.GetId()] = &commonv1.Cluster{
		Id: msg.GetId(), Name: msg.GetName(), CustomerId: msg.GetCustomerId(),
		KubeconfigRef: msg.GetKubeconfigRef(), Status: commonv1.ClusterStatus_CLUSTER_STATUS_ACTIVE,
	}
	return connect.NewResponse(&orchestratorv1.CreateClusterResponse{Cluster: f.clusters[msg.GetId()]}), nil
}

func (f *fakeOrchestrator) ConfigureClusterRoute(_ context.Context, req *connect.Request[orchestratorv1.ConfigureClusterRouteRequest]) (*connect.Response[orchestratorv1.ConfigureClusterRouteResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("ConfigureClusterRoute")
	msg := req.Msg
	route, ok := f.routes[msg.GetId()]
	if !ok {
		route = &orchestratorv1.ClusterRoute{Id: msg.GetId()}
		f.routes[msg.GetId()] = route
	}
	route.ClusterId = msg.GetClusterId()
	route.ArtifactType = msg.GetArtifactType()
	route.Mode = msg.GetMode()
	route.SourcePrefix = msg.GetSourcePrefix()
	route.TargetPrefix = msg.GetTargetPrefix()
	return connect.NewResponse(&orchestratorv1.ConfigureClusterRouteResponse{Route: route}), nil
}

func (f *fakeOrchestrator) GetClusterRoutes(_ context.Context, req *connect.Request[orchestratorv1.GetClusterRoutesRequest]) (*connect.Response[orchestratorv1.GetClusterRoutesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("GetClusterRoutes")
	var out []*orchestratorv1.ClusterRoute
	for _, route := range f.routes {
		if route.GetClusterId() == req.Msg.GetClusterId() {
			out = append(out, route)
		}
	}
	return connect.NewResponse(&orchestratorv1.GetClusterRoutesResponse{Routes: out}), nil
}

func (f *fakeOrchestrator) ListReleaseDefinitions(_ context.Context, req *connect.Request[orchestratorv1.ListReleaseDefinitionsRequest]) (*connect.Response[orchestratorv1.ListReleaseDefinitionsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("ListReleaseDefinitions")
	var out []*commonv1.ReleaseDefinition
	for _, definition := range f.definitions {
		if definition.GetClusterId() == req.Msg.GetClusterId() {
			out = append(out, definition)
		}
	}
	return connect.NewResponse(&orchestratorv1.ListReleaseDefinitionsResponse{Definitions: out}), nil
}

func (f *fakeOrchestrator) CreateReleaseDefinition(_ context.Context, req *connect.Request[orchestratorv1.CreateReleaseDefinitionRequest]) (*connect.Response[orchestratorv1.CreateReleaseDefinitionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("CreateReleaseDefinition")
	msg := req.Msg
	id := fmt.Sprintf("definition-%d", len(f.definitions)+1)
	definition := &commonv1.ReleaseDefinition{
		Id: id, Name: msg.GetReleaseName(), CustomerId: msg.GetCustomerId(),
		ClusterId: msg.GetClusterId(), Namespace: msg.GetNamespace(),
		ReleaseName: msg.GetReleaseName(), ChartName: msg.GetChartName(),
		Status: "active",
	}
	f.definitions[id] = definition
	return connect.NewResponse(&orchestratorv1.CreateReleaseDefinitionResponse{Definition: definition}), nil
}

func (f *fakeOrchestrator) CreateValuesRevision(_ context.Context, req *connect.Request[orchestratorv1.CreateValuesRevisionRequest]) (*connect.Response[orchestratorv1.CreateValuesRevisionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("CreateValuesRevision")
	for _, revision := range f.values {
		if revision.GetReleaseDefinitionId() == req.Msg.GetReleaseDefinitionId() && revision.GetDigest() == valuesDigest([]byte(req.Msg.GetDocument())) {
			return connect.NewResponse(&orchestratorv1.CreateValuesRevisionResponse{Revision: revision, Created: false}), nil
		}
	}
	id := fmt.Sprintf("values-%d", len(f.values)+1)
	revision := &commonv1.ValuesRevision{
		Id: id, ReleaseDefinitionId: req.Msg.GetReleaseDefinitionId(),
		CanonicalDocument: []byte(req.Msg.GetDocument()), Digest: valuesDigest([]byte(req.Msg.GetDocument())),
		Status: commonv1.ValuesStatus_VALUES_STATUS_DRAFT, StateVersion: 1,
	}
	f.values[id] = revision
	return connect.NewResponse(&orchestratorv1.CreateValuesRevisionResponse{Revision: revision, Created: true}), nil
}

func (f *fakeOrchestrator) ListValuesRevisions(_ context.Context, req *connect.Request[orchestratorv1.ListValuesRevisionsRequest]) (*connect.Response[orchestratorv1.ListValuesRevisionsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("ListValuesRevisions")
	var out []*commonv1.ValuesRevision
	for _, revision := range f.values {
		if revision.GetReleaseDefinitionId() == req.Msg.GetReleaseDefinitionId() {
			out = append(out, revision)
		}
	}
	return connect.NewResponse(&orchestratorv1.ListValuesRevisionsResponse{Items: out}), nil
}

func (f *fakeOrchestrator) decision(revision *commonv1.ValuesRevision, next commonv1.ValuesStatus) *connect.Response[orchestratorv1.ValuesRevisionDecisionResponse] {
	previous := revision.GetStatus()
	revision.Status = next
	revision.StateVersion++
	return connect.NewResponse(&orchestratorv1.ValuesRevisionDecisionResponse{
		Revision: revision, PreviousState: previous, NewState: next,
	})
}

func (f *fakeOrchestrator) SubmitValuesRevision(_ context.Context, req *connect.Request[orchestratorv1.SubmitValuesRevisionRequest]) (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("SubmitValuesRevision")
	revision, ok := f.values[req.Msg.GetRevisionId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("revision not found"))
	}
	return f.decision(revision, commonv1.ValuesStatus_VALUES_STATUS_PENDING_APPROVAL), nil
}

func (f *fakeOrchestrator) ApproveValuesRevision(_ context.Context, req *connect.Request[orchestratorv1.ApproveValuesRevisionRequest]) (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("ApproveValuesRevision")
	revision, ok := f.values[req.Msg.GetRevisionId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("revision not found"))
	}
	return f.decision(revision, commonv1.ValuesStatus_VALUES_STATUS_APPROVED), nil
}

func (f *fakeOrchestrator) CreateEnrollmentToken(_ context.Context, req *connect.Request[orchestratorv1.CreateEnrollmentTokenRequest]) (*connect.Response[orchestratorv1.CreateEnrollmentTokenResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("CreateEnrollmentToken")
	token := fmt.Sprintf("enroll-%s-%d", req.Msg.GetClusterId(), len(f.tokens)+1)
	f.tokens[req.Msg.GetClusterId()] = token
	return connect.NewResponse(&orchestratorv1.CreateEnrollmentTokenResponse{
		Token: token, CustomerId: req.Msg.GetCustomerId(), ClusterId: req.Msg.GetClusterId(),
	}), nil
}

func (f *fakeOrchestrator) CreateOperation(_ context.Context, req *connect.Request[orchestratorv1.CreateOperationRequest]) (*connect.Response[orchestratorv1.CreateOperationResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("CreateOperation")
	f.nextOpID++
	terminal := orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED
	if override, ok := f.opKeyTerminal[req.Msg.GetIdempotencyKey()]; ok {
		terminal = override
	}
	op := &fakeOperation{
		id:       fmt.Sprintf("op-%d", f.nextOpID),
		key:      req.Msg.GetIdempotencyKey(),
		state:    orchestratorv1.OperationStatus_OPERATION_STATUS_PENDING,
		terminal: terminal,
	}
	f.operations[op.id] = op
	return connect.NewResponse(&orchestratorv1.CreateOperationResponse{
		OperationId: op.id, State: op.state.String(),
	}), nil
}

func (f *fakeOrchestrator) GetOperation(_ context.Context, req *connect.Request[orchestratorv1.GetOperationRequest]) (*connect.Response[orchestratorv1.GetOperationResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bump("GetOperation")
	op, ok := f.operations[req.Msg.GetOperationId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("operation not found"))
	}
	op.advance()
	return connect.NewResponse(&orchestratorv1.GetOperationResponse{
		Operation: &orchestratorv1.Operation{OperationId: op.id, State: op.state},
	}), nil
}

// fakeTrust is an in-memory TrustService.
type fakeTrust struct {
	mu     sync.Mutex
	roots  map[string][]*trustv1.TrustRoot
	nextID int
	calls  map[string]int
}

func newFakeTrust() *fakeTrust {
	return &fakeTrust{roots: map[string][]*trustv1.TrustRoot{}, calls: map[string]int{}}
}

func (f *fakeTrust) GetTrustPolicy(_ context.Context, req *connect.Request[trustv1.GetTrustPolicyRequest]) (*connect.Response[trustv1.GetTrustPolicyResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["GetTrustPolicy"]++
	return connect.NewResponse(&trustv1.GetTrustPolicyResponse{
		Policy: &trustv1.TrustPolicy{Environment: req.Msg.GetEnvironment(), Version: 1, Roots: f.roots[req.Msg.GetEnvironment()]},
	}), nil
}

func (f *fakeTrust) CreateTrustRoot(_ context.Context, req *connect.Request[trustv1.CreateTrustRootRequest]) (*connect.Response[trustv1.CreateTrustRootResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["CreateTrustRoot"]++
	f.nextID++
	root := &trustv1.TrustRoot{
		Id: fmt.Sprintf("root-%d", f.nextID), Environment: req.Msg.GetEnvironment(),
		KeyId: req.Msg.GetKeyId(), PublicKeyPem: req.Msg.GetPublicKeyPem(),
		Issuer: req.Msg.GetIssuer(), SubjectPattern: req.Msg.GetSubjectPattern(),
		State: trustv1.TrustRootState_TRUST_ROOT_STATE_ACTIVE,
	}
	f.roots[req.Msg.GetEnvironment()] = append(f.roots[req.Msg.GetEnvironment()], root)
	return connect.NewResponse(&trustv1.CreateTrustRootResponse{
		Policy: &trustv1.TrustPolicy{Environment: req.Msg.GetEnvironment(), Version: 1, Roots: f.roots[req.Msg.GetEnvironment()]},
		Root:   root,
	}), nil
}

// fakeWebhook submits release bundles, deduping by chart digest.
type fakeWebhook struct {
	mu        sync.Mutex
	summaries map[string]*orchestratorv1.BundleSummary // key: chart digest
	shared    map[string]*orchestratorv1.BundleSummary // shared with fakeBundle
	nextID    int
	calls     int
	failOnce  error
	failEvery error
}

func newFakeWebhook(shared map[string]*orchestratorv1.BundleSummary) *fakeWebhook {
	return &fakeWebhook{summaries: map[string]*orchestratorv1.BundleSummary{}, shared: shared}
}

func (f *fakeWebhook) SubmitReleaseBundle(_ context.Context, req *connect.Request[webhookv1.SubmitReleaseBundleRequest]) (*connect.Response[webhookv1.SubmitReleaseBundleResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failOnce != nil {
		err := f.failOnce
		f.failOnce = nil
		return nil, err
	}
	if f.failEvery != nil {
		return nil, f.failEvery
	}
	msg := req.Msg
	digest := "sha256:" + valuesDigest([]byte(msg.GetName()+"|"+msg.GetChartDigest()))
	summary, ok := f.summaries[msg.GetChartDigest()]
	if !ok {
		f.nextID++
		summary = &orchestratorv1.BundleSummary{
			Id: fmt.Sprintf("bundle-%d", f.nextID), Name: msg.GetName(),
			Digest:   &commonv1.ReleaseDigest{Algorithm: "sha256", Value: digest},
			ChartRef: msg.GetChartRef(), ChartVersion: msg.GetChartVersion(), ChartDigest: msg.GetChartDigest(),
			Status: commonv1.BundleStatus_BUNDLE_STATUS_RECEIVED,
		}
		f.summaries[msg.GetChartDigest()] = summary
		f.shared[summary.GetId()] = summary
	}
	return connect.NewResponse(&webhookv1.SubmitReleaseBundleResponse{Bundle: summary, Created: !ok}), nil
}

// fakeBundle serves GetBundle for verify.
type fakeBundle struct {
	mu        sync.Mutex
	summaries map[string]*orchestratorv1.BundleSummary
}

func newFakeBundle() *fakeBundle {
	return &fakeBundle{summaries: map[string]*orchestratorv1.BundleSummary{}}
}

func (f *fakeBundle) GetBundle(_ context.Context, req *connect.Request[orchestratorv1.GetBundleRequest]) (*connect.Response[orchestratorv1.GetBundleResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	summary, ok := f.summaries[req.Msg.GetBundleId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("bundle not found"))
	}
	return connect.NewResponse(&orchestratorv1.GetBundleResponse{
		Bundle: &orchestratorv1.BundleDetail{Summary: summary},
	}), nil
}

func (f *fakeBundle) ListBundles(_ context.Context, _ *connect.Request[orchestratorv1.ListBundlesRequest]) (*connect.Response[orchestratorv1.ListBundlesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*orchestratorv1.BundleSummary
	for _, summary := range f.summaries {
		out = append(out, summary)
	}
	return connect.NewResponse(&orchestratorv1.ListBundlesResponse{Bundles: out}), nil
}

// fakeServices bundles every fake seam.
type fakeServices struct {
	auth    *fakeAuth
	binding *fakeBinding
	orch    *fakeOrchestrator
	trust   *fakeTrust
	webhook *fakeWebhook
	bundle  *fakeBundle
}

func newFakeServices() *fakeServices {
	bundle := newFakeBundle()
	return &fakeServices{
		auth:    newFakeAuth(),
		binding: newFakeBinding(),
		orch:    newFakeOrchestrator(),
		trust:   newFakeTrust(),
		webhook: newFakeWebhook(bundle.summaries),
		bundle:  bundle,
	}
}
