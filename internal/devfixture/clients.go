package devfixture

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	trustv1 "github.com/ndzuki/release-manager/api/gen/trust/v1"
	trustv1connect "github.com/ndzuki/release-manager/api/gen/trust/v1/trustv1connect"
	webhookv1 "github.com/ndzuki/release-manager/api/gen/webhook/v1"
	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
)

// The narrow service seams below are exactly the RPCs the seed runner uses.
// The generated Connect client interfaces satisfy them structurally, and
// tests inject hand-written fakes with identical signatures.

// AuthClient is the auth service seam (accounts phase + login preamble).
type AuthClient interface {
	GetInitStatus(context.Context, *connect.Request[authv1.GetInitStatusRequest]) (*connect.Response[authv1.GetInitStatusResponse], error)
	Initialize(context.Context, *connect.Request[authv1.InitializeRequest]) (*connect.Response[authv1.InitializeResponse], error)
	Login(context.Context, *connect.Request[authv1.LoginRequest]) (*connect.Response[authv1.LoginResponse], error)
	CreateLocalUser(context.Context, *connect.Request[authv1.CreateLocalUserRequest]) (*connect.Response[authv1.CreateLocalUserResponse], error)
	GetLocalUser(context.Context, *connect.Request[authv1.GetLocalUserRequest]) (*connect.Response[authv1.GetLocalUserResponse], error)
}

// BindingClient is the org-customer binding seam (identity phase).
type BindingClient interface {
	ListBindings(context.Context, *connect.Request[authv1.ListBindingsRequest]) (*connect.Response[authv1.ListBindingsResponse], error)
	CreateBinding(context.Context, *connect.Request[authv1.CreateBindingRequest]) (*connect.Response[authv1.CreateBindingResponse], error)
}

// OrchestratorClient is the orchestrator seam covering every entity the seed
// creates. Methods are grouped by phase in the code comments.
type OrchestratorClient interface {
	// identity
	ListCustomers(context.Context, *connect.Request[orchestratorv1.ListCustomersRequest]) (*connect.Response[orchestratorv1.ListCustomersResponse], error)
	CreateCustomer(context.Context, *connect.Request[orchestratorv1.CreateCustomerRequest]) (*connect.Response[orchestratorv1.CreateCustomerResponse], error)
	GetCluster(context.Context, *connect.Request[orchestratorv1.GetClusterRequest]) (*connect.Response[orchestratorv1.GetClusterResponse], error)
	CreateCluster(context.Context, *connect.Request[orchestratorv1.CreateClusterRequest]) (*connect.Response[orchestratorv1.CreateClusterResponse], error)
	// routing
	ConfigureClusterRoute(context.Context, *connect.Request[orchestratorv1.ConfigureClusterRouteRequest]) (*connect.Response[orchestratorv1.ConfigureClusterRouteResponse], error)
	GetClusterRoutes(context.Context, *connect.Request[orchestratorv1.GetClusterRoutesRequest]) (*connect.Response[orchestratorv1.GetClusterRoutesResponse], error)
	// values
	ListReleaseDefinitions(context.Context, *connect.Request[orchestratorv1.ListReleaseDefinitionsRequest]) (*connect.Response[orchestratorv1.ListReleaseDefinitionsResponse], error)
	CreateReleaseDefinition(context.Context, *connect.Request[orchestratorv1.CreateReleaseDefinitionRequest]) (*connect.Response[orchestratorv1.CreateReleaseDefinitionResponse], error)
	CreateValuesRevision(context.Context, *connect.Request[orchestratorv1.CreateValuesRevisionRequest]) (*connect.Response[orchestratorv1.CreateValuesRevisionResponse], error)
	ListValuesRevisions(context.Context, *connect.Request[orchestratorv1.ListValuesRevisionsRequest]) (*connect.Response[orchestratorv1.ListValuesRevisionsResponse], error)
	SubmitValuesRevision(context.Context, *connect.Request[orchestratorv1.SubmitValuesRevisionRequest]) (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error)
	ApproveValuesRevision(context.Context, *connect.Request[orchestratorv1.ApproveValuesRevisionRequest]) (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error)
	// enrollment
	CreateEnrollmentToken(context.Context, *connect.Request[orchestratorv1.CreateEnrollmentTokenRequest]) (*connect.Response[orchestratorv1.CreateEnrollmentTokenResponse], error)
	// install
	CreateOperation(context.Context, *connect.Request[orchestratorv1.CreateOperationRequest]) (*connect.Response[orchestratorv1.CreateOperationResponse], error)
	GetOperation(context.Context, *connect.Request[orchestratorv1.GetOperationRequest]) (*connect.Response[orchestratorv1.GetOperationResponse], error)
}

// TrustClient is the trust service seam (trust phase).
type TrustClient interface {
	GetTrustPolicy(context.Context, *connect.Request[trustv1.GetTrustPolicyRequest]) (*connect.Response[trustv1.GetTrustPolicyResponse], error)
	CreateTrustRoot(context.Context, *connect.Request[trustv1.CreateTrustRootRequest]) (*connect.Response[trustv1.CreateTrustRootResponse], error)
}

// WebhookClient is the webhook service seam (bundle phase).
type WebhookClient interface {
	SubmitReleaseBundle(context.Context, *connect.Request[webhookv1.SubmitReleaseBundleRequest]) (*connect.Response[webhookv1.SubmitReleaseBundleResponse], error)
}

// BundleClient is the orchestrator bundle service seam (verify phase).
type BundleClient interface {
	GetBundle(context.Context, *connect.Request[orchestratorv1.GetBundleRequest]) (*connect.Response[orchestratorv1.GetBundleResponse], error)
	ListBundles(context.Context, *connect.Request[orchestratorv1.ListBundlesRequest]) (*connect.Response[orchestratorv1.ListBundlesResponse], error)
}

// connectClients bundles the production Connect clients. Fakes implement the
// narrow seams above; the generated clients satisfy them structurally.
type connectClients struct {
	auth    AuthClient
	binding BindingClient
	orch    OrchestratorClient
	trust   TrustClient
	webhook WebhookClient
	bundle  BundleClient
}

func newConnectClients(cfg Config) *connectClients {
	httpClient := http.DefaultClient
	return &connectClients{
		auth:    authv1connect.NewAuthServiceClient(httpClient, cfg.AuthURL),
		binding: authv1connect.NewBindingServiceClient(httpClient, cfg.AuthURL),
		orch:    orchestratorv1connect.NewOrchestratorServiceClient(httpClient, cfg.OrchestratorURL),
		trust:   trustv1connect.NewTrustServiceClient(httpClient, cfg.OrchestratorURL),
		webhook: webhookv1connect.NewWebhookServiceClient(httpClient, cfg.WebhookURL),
		bundle:  orchestratorv1connect.NewBundleServiceClient(httpClient, cfg.OrchestratorURL),
	}
}

// withAuth injects the bearer token into a Connect request.
func withAuth[T any](req *connect.Request[T], token string) {
	req.Header().Set("Authorization", "Bearer "+token)
}
