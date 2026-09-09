package e2e

import (
	"net/http"
	"strings"

	"connectrpc.com/connect"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	notifierv1connect "github.com/ndzuki/release-manager/api/gen/notifier/v1/notifierv1connect"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	trustv1connect "github.com/ndzuki/release-manager/api/gen/trust/v1/trustv1connect"
	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
)

// ClientBundle contains the generated Connect clients used by E2E stages.
//
// Client fields are excluded from JSON serialization because they are runtime
// handles, not run artifacts. Credentials, tokens, and password values are not
// stored in this bundle.
type ClientBundle struct {
	auth             authv1connect.AuthServiceClient
	organization     authv1connect.OrganizationServiceClient
	binding          authv1connect.BindingServiceClient
	authorization    authv1connect.AuthorizationServiceClient
	externalIdentity authv1connect.ExternalIdentityServiceClient

	orchestrator orchestratorv1connect.OrchestratorServiceClient
	bundle       orchestratorv1connect.BundleServiceClient
	cleanup      orchestratorv1connect.CleanupServiceClient
	trust        trustv1connect.TrustServiceClient

	webhook  webhookv1connect.WebhookServiceClient
	operator operatorv1connect.OperatorServiceClient
	notifier notifierv1connect.NotifierServiceClient

	httpClient         connect.HTTPClient
	operatorHTTPClient connect.HTTPClient
}

// Clients is a compatibility alias for callers that prefer the shorter name.
type Clients = ClientBundle

// ClientOptions controls the HTTP transports used by generated clients. The
// operator transport may be supplied separately so callers can install the
// operator gateway's HTTPS/mTLS transport without changing other clients.
type ClientOptions struct {
	HTTPClient         connect.HTTPClient
	OperatorHTTPClient connect.HTTPClient
}

// NewClientBundle constructs all E2E Connect clients from the declared service
// endpoints. It validates every endpoint needed by the control-plane checks;
// missing endpoints fail closed before any client is constructed.
func NewClientBundle(cfg *Config) (*ClientBundle, error) {
	return NewClientBundleWithOptions(cfg, ClientOptions{})
}

// NewClients is an alias for NewClientBundle.
func NewClients(cfg *Config) (*ClientBundle, error) { return NewClientBundle(cfg) }

// NewConnectClients is an explicit alias for NewClientBundle.
func NewConnectClients(cfg *Config) (*ClientBundle, error) { return NewClientBundle(cfg) }

// NewClientBundleWithHTTPClient constructs clients with one injected HTTP
// transport. It is useful for deterministic tests and local in-process probes.
func NewClientBundleWithHTTPClient(cfg *Config, httpClient connect.HTTPClient) (*ClientBundle, error) {
	return NewClientBundleWithOptions(cfg, ClientOptions{HTTPClient: httpClient})
}

// NewClientBundleWithOptions constructs clients with separate general and
// operator transports.
func NewClientBundleWithOptions(cfg *Config, opts ClientOptions) (*ClientBundle, error) {
	if cfg == nil {
		return nil, configInvalid("config", "nil config")
	}
	if err := validateClientEndpoints(cfg.Endpoints); err != nil {
		return nil, err
	}

	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	operatorClient := opts.OperatorHTTPClient
	if operatorClient == nil {
		operatorClient = client
	}

	return &ClientBundle{
		auth:             authv1connect.NewAuthServiceClient(client, cfg.Endpoints.ReleaseAuth),
		organization:     authv1connect.NewOrganizationServiceClient(client, cfg.Endpoints.ReleaseAuth),
		binding:          authv1connect.NewBindingServiceClient(client, cfg.Endpoints.ReleaseAuth),
		authorization:    authv1connect.NewAuthorizationServiceClient(client, cfg.Endpoints.ReleaseAuth),
		externalIdentity: authv1connect.NewExternalIdentityServiceClient(client, cfg.Endpoints.ReleaseAuth),

		orchestrator: orchestratorv1connect.NewOrchestratorServiceClient(client, cfg.Endpoints.ReleaseOrchestrator),
		bundle:       orchestratorv1connect.NewBundleServiceClient(client, cfg.Endpoints.ReleaseOrchestrator),
		cleanup:      orchestratorv1connect.NewCleanupServiceClient(client, cfg.Endpoints.ReleaseOrchestrator),
		trust:        trustv1connect.NewTrustServiceClient(client, cfg.Endpoints.ReleaseOrchestrator),

		webhook:  webhookv1connect.NewWebhookServiceClient(client, cfg.Endpoints.ReleaseWebhook),
		operator: operatorv1connect.NewOperatorServiceClient(operatorClient, cfg.Endpoints.ReleaseOperator),
		notifier: notifierv1connect.NewNotifierServiceClient(client, cfg.Endpoints.ReleaseNotifier),

		httpClient:         client,
		operatorHTTPClient: operatorClient,
	}, nil
}

func validateClientEndpoints(endpoints Endpoints) error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "release_orchestrator", value: endpoints.ReleaseOrchestrator},
		{name: "release_webhook", value: endpoints.ReleaseWebhook},
		{name: "release_operator", value: endpoints.ReleaseOperator},
		{name: "release_auth", value: endpoints.ReleaseAuth},
		{name: "release_notifier", value: endpoints.ReleaseNotifier},
		{name: "release_api", value: endpoints.ReleaseAPI},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			return configInvalid("endpoints."+field.name, "missing")
		}
		if err := validateEndpoint(field.value); err != nil {
			return configInvalid("endpoints."+field.name, "must be an absolute http or https URL")
		}
	}
	return nil
}
