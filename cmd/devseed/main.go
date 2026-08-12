// Package main seeds deterministic development data through public service seams.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	webhookv1 "github.com/ndzuki/release-manager/api/gen/webhook/v1"
	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
)

const (
	defaultOrchestratorURL = "http://localhost:8083"
	defaultWebhookURL      = "http://localhost:8082"
	defaultAuthURL         = "http://localhost:8085"
	developmentNamespace   = "release-manager-dev"
	// devEnrollmentTokenDir holds one 0600 enrollment token file per dev
	// cluster (TASK-075 plan v1 Step 8); TASK-065 replaces this with Secret
	// injection into the customer clusters.
	devEnrollmentTokenDir = "data/dev-enrollment-tokens" //nolint:gosec // directory path, not a credential
)

type seedConfig struct {
	orchestratorURL string
	webhookURL      string
	authURL         string
	adminUser       string
	adminPassword   string
	deployerUser    string
	deployerPass    string
}

type routeSeed struct {
	id           string
	clusterID    string
	artifactType orchestratorv1.ArtifactType
	mode         orchestratorv1.ArtifactMode
	sourcePrefix string
	targetPrefix string
}

func main() {
	cfg := seedConfig{}
	flag.StringVar(&cfg.orchestratorURL, "orchestrator", defaultOrchestratorURL, "Orchestrator Connect URL")
	flag.StringVar(&cfg.webhookURL, "webhook", defaultWebhookURL, "Webhook Connect URL")
	flag.StringVar(&cfg.authURL, "auth", defaultAuthURL, "Auth Connect URL")
	flag.StringVar(&cfg.adminUser, "admin-user", envOr("DEV_ADMIN_USER", "admin"), "platform admin username (env DEV_ADMIN_USER)")
	flag.StringVar(&cfg.adminPassword, "admin-password", os.Getenv("DEV_ADMIN_PASSWORD"), "platform admin password (env DEV_ADMIN_PASSWORD)")
	flag.StringVar(&cfg.deployerUser, "deployer-user", envOr("DEV_DEPLOYER_USER", "deployer"), "deployer username (env DEV_DEPLOYER_USER)")
	flag.StringVar(&cfg.deployerPass, "deployer-password", os.Getenv("DEV_DEPLOYER_PASSWORD"), "deployer password (env DEV_DEPLOYER_PASSWORD)")
	flag.Parse()

	if cfg.adminPassword == "" {
		fail("admin password is required (--admin-password or DEV_ADMIN_PASSWORD)")
	}
	if cfg.deployerPass == "" {
		fail("deployer password is required (--deployer-password or DEV_DEPLOYER_PASSWORD)")
	}

	if err := run(context.Background(), cfg); err != nil {
		slog.Error("development seed failed", "error", err)
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
	fmt.Println("development seed complete")
	fmt.Printf("namespace: %s\n", developmentNamespace)
	fmt.Printf("orchestrator: %s\n", strings.TrimRight(cfg.orchestratorURL, "/"))
	fmt.Printf("webhook: %s\n", strings.TrimRight(cfg.webhookURL, "/"))
	fmt.Printf("auth: %s\n", strings.TrimRight(cfg.authURL, "/"))
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "error:", message)
	os.Exit(1)
}

func run(ctx context.Context, cfg seedConfig) error {
	httpClient := http.DefaultClient
	authClient := authv1connect.NewAuthServiceClient(httpClient, cfg.authURL)
	adminToken, deployerToken, err := ensureAuth(ctx, authClient, cfg.adminUser, cfg.adminPassword, cfg.deployerUser, cfg.deployerPass)
	if err != nil {
		return err
	}
	orchestratorClient := orchestratorv1connect.NewOrchestratorServiceClient(httpClient, cfg.orchestratorURL)
	webhookClient := webhookv1connect.NewWebhookServiceClient(httpClient, cfg.webhookURL)

	customers := []struct {
		id   string
		name string
		slug string
	}{
		{id: "dev-customer-a", name: "Development Customer A", slug: "development-customer-a"},
		{id: "dev-customer-b", name: "Development Customer B", slug: "development-customer-b"},
	}

	for _, customer := range customers {
		if err := ensureCustomer(ctx, orchestratorClient, deployerToken, customer.id, customer.name, customer.slug); err != nil {
			return err
		}
	}

	clusters := []clusterSeed{
		{id: "dev-customer-a-direct", name: "Customer A Direct", customerID: "dev-customer-a"},
		{id: "dev-customer-a-cache", name: "Customer A Pull Through Cache", customerID: "dev-customer-a"},
		{id: "dev-customer-b-replicated", name: "Customer B Replicated", customerID: "dev-customer-b"},
		{id: "dev-customer-b-mixed", name: "Customer B Mixed", customerID: "dev-customer-b"},
	}

	for _, cluster := range clusters {
		if err := ensureCluster(ctx, orchestratorClient, deployerToken, cluster.id, cluster.name, cluster.customerID); err != nil {
			return err
		}
	}
	if err := ensureEnrollmentTokens(ctx, orchestratorClient, deployerToken, clusters, devEnrollmentTokenDir); err != nil {
		return err
	}

	routes := []routeSeed{
		{id: "route-a-direct-image", clusterID: clusters[0].id, artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT, sourcePrefix: "docker.io/release-manager", targetPrefix: "docker.io/release-manager"},
		{id: "route-a-direct-chart", clusterID: clusters[0].id, artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT, sourcePrefix: "charts.example.dev", targetPrefix: "charts.example.dev"},
		{id: "route-a-cache-image", clusterID: clusters[1].id, artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_PULL_THROUGH_CACHE, sourcePrefix: "docker.io/release-manager", targetPrefix: "cache.release-manager-dev"},
		{id: "route-a-cache-chart", clusterID: clusters[1].id, artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED, sourcePrefix: "charts.example.dev", targetPrefix: "registry.release-manager-dev/charts"},
		{id: "route-b-replicated-image", clusterID: clusters[2].id, artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED, sourcePrefix: "docker.io/release-manager", targetPrefix: "registry.release-manager-dev/images"},
		{id: "route-b-replicated-chart", clusterID: clusters[2].id, artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED, sourcePrefix: "charts.example.dev", targetPrefix: "registry.release-manager-dev/charts"},
		{id: "route-b-mixed-image", clusterID: clusters[3].id, artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_IMAGE, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_DIRECT, sourcePrefix: "docker.io/release-manager", targetPrefix: "docker.io/release-manager"},
		{id: "route-b-mixed-chart", clusterID: clusters[3].id, artifactType: orchestratorv1.ArtifactType_ARTIFACT_TYPE_CHART, mode: orchestratorv1.ArtifactMode_ARTIFACT_MODE_REPLICATED, sourcePrefix: "charts.example.dev", targetPrefix: "registry.release-manager-dev/charts"},
	}
	for _, route := range routes {
		if err := ensureRoute(ctx, orchestratorClient, deployerToken, route); err != nil {
			return err
		}
	}

	definitions := make([]string, 0, len(clusters))
	for i, cluster := range clusters {
		definitionID, err := ensureDefinition(ctx, orchestratorClient, deployerToken, cluster.customerID, cluster.id, i)
		if err != nil {
			return err
		}
		definitions = append(definitions, definitionID)
	}

	for i, definitionID := range definitions {
		if err := ensureValuesRevision(ctx, orchestratorClient, deployerToken, adminToken, definitionID, i); err != nil {
			return err
		}
	}

	if err := ensureReleaseBundle(ctx, webhookClient); err != nil {
		return err
	}
	return nil
}

// ensureAuth authenticates the admin and deployer actors, initializing the
// system when no admin user exists yet (REQ-065 credentials model). When the
// deployer login fails, the account is provisioned via the formal
// CreateLocalUser seam (TASK-072, platform_admin-only, bound to the admin's
// active organization) and the login is retried once — the RPC is idempotent
// on the username natural key (D-13), so re-seeding never recreates the user.
// Provisioning failure still fails closed with an explicit error (per ADR-013
// seed only uses public service seams).
func ensureAuth(
	ctx context.Context,
	client authv1connect.AuthServiceClient,
	adminUser, adminPassword, deployerUser, deployerPassword string,
) (adminToken, deployerToken string, err error) {
	initStatus, err := client.GetInitStatus(ctx, connect.NewRequest(&authv1.GetInitStatusRequest{}))
	if err != nil {
		return "", "", fmt.Errorf("get init status: %w", err)
	}
	if !initStatus.Msg.GetInitialized() {
		if _, err := client.Initialize(ctx, connect.NewRequest(&authv1.InitializeRequest{
			Username:         adminUser,
			Password:         adminPassword,
			OrganizationName: developmentNamespace,
		})); err != nil {
			return "", "", fmt.Errorf("initialize system: %w", err)
		}
		fmt.Printf("system initialized with admin user %s\n", adminUser)
	}
	adminLogin, err := client.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: adminUser, Password: adminPassword}))
	if err != nil {
		return "", "", fmt.Errorf("login admin user %s: %w", adminUser, err)
	}
	adminToken = adminLogin.Msg.GetAccessToken()

	deployerLogin, err := client.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: deployerUser, Password: deployerPassword}))
	if err == nil {
		return adminToken, deployerLogin.Msg.GetAccessToken(), nil
	}
	createReq := connect.NewRequest(&authv1.CreateLocalUserRequest{
		Username: deployerUser,
		Password: deployerPassword,
		Roles:    []string{"deployer"},
	})
	withAuth(createReq, adminToken)
	if _, createErr := client.CreateLocalUser(ctx, createReq); createErr != nil {
		return "", "", fmt.Errorf("login deployer user %s: %w; provision via CreateLocalUser: %v", deployerUser, err, createErr)
	}
	fmt.Printf("deployer user %s created\n", deployerUser)
	deployerLogin, err = client.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: deployerUser, Password: deployerPassword}))
	if err != nil {
		return "", "", fmt.Errorf("login deployer user %s after provisioning: %w", deployerUser, err)
	}
	return adminToken, deployerLogin.Msg.GetAccessToken(), nil
}

// withAuth injects the bearer token into a Connect request.
func withAuth[T any](req *connect.Request[T], token string) {
	req.Header().Set("Authorization", "Bearer "+token)
}

func ensureCustomer(ctx context.Context, client orchestratorv1connect.OrchestratorServiceClient, token, id, name, slug string) error {
	req := connect.NewRequest(&orchestratorv1.GetCustomerRequest{CustomerId: id})
	withAuth(req, token)
	_, err := client.GetCustomer(ctx, req)
	if err == nil {
		fmt.Printf("customer %s already exists\n", id)
		return nil
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		return fmt.Errorf("get customer %s: %w", id, err)
	}
	createReq := connect.NewRequest(&orchestratorv1.CreateCustomerRequest{Id: id, Name: name, Slug: slug})
	withAuth(createReq, token)
	_, err = client.CreateCustomer(ctx, createReq)
	if err != nil {
		return fmt.Errorf("create customer %s: %w", id, err)
	}
	fmt.Printf("customer %s created\n", id)
	return nil
}

func ensureCluster(ctx context.Context, client orchestratorv1connect.OrchestratorServiceClient, token, id, name, customerID string) error {
	req := connect.NewRequest(&orchestratorv1.GetClusterRequest{ClusterId: id})
	withAuth(req, token)
	_, err := client.GetCluster(ctx, req)
	if err == nil {
		fmt.Printf("cluster %s already exists\n", id)
		return nil
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		return fmt.Errorf("get cluster %s: %w", id, err)
	}
	createReq := connect.NewRequest(&orchestratorv1.CreateClusterRequest{Id: id, Name: name, CustomerId: customerID, KubeconfigRef: "kind://release-manager-dev"})
	withAuth(createReq, token)
	_, err = client.CreateCluster(ctx, createReq)
	if err != nil {
		return fmt.Errorf("create cluster %s: %w", id, err)
	}
	fmt.Printf("cluster %s created\n", id)
	return nil
}

// enrollmentTokenCreator is the narrow CreateEnrollmentToken seam used by
// the enrollment seed stage.
type enrollmentTokenCreator interface {
	CreateEnrollmentToken(context.Context, *connect.Request[orchestratorv1.CreateEnrollmentTokenRequest]) (*connect.Response[orchestratorv1.CreateEnrollmentTokenResponse], error)
}

// clusterSeed identifies one dev cluster for seeding.
type clusterSeed struct {
	id         string
	name       string
	customerID string
}

// ensureEnrollmentTokens creates one pending enrollment token per dev
// cluster and writes it to a 0600 file (REQ-065: devseed injects the token
// via Secret into each customer cluster; the file is the local stand-in
// until TASK-065 wires the Secret). Re-running dev-up is idempotent: an
// existing token file is left untouched, so a consumed token stays consumed
// and a live token is not regenerated.
func ensureEnrollmentTokens(ctx context.Context, client enrollmentTokenCreator, token string, clusters []clusterSeed, tokenDir string) error {
	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		return fmt.Errorf("create enrollment token dir: %w", err)
	}
	for _, cluster := range clusters {
		if err := ensureEnrollmentToken(ctx, client, token, cluster.id, cluster.customerID, tokenDir); err != nil {
			return err
		}
	}
	return nil
}

// ensureEnrollmentToken writes the enrollment token file for one cluster.
// The token value appears only in the CreateEnrollmentToken response and is
// never logged (REQ-065 log safety).
func ensureEnrollmentToken(ctx context.Context, client enrollmentTokenCreator, token, clusterID, customerID, tokenDir string) error {
	tokenPath := filepath.Join(tokenDir, clusterID+".token")
	if _, err := os.Stat(tokenPath); err == nil {
		fmt.Printf("enrollment token for cluster %s already seeded\n", clusterID)
		return nil
	}
	req := connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
		CustomerId: customerID,
		ClusterId:  clusterID,
	})
	withAuth(req, token)
	response, err := client.CreateEnrollmentToken(ctx, req)
	if err != nil {
		return fmt.Errorf("create enrollment token for cluster %s: %w", clusterID, err)
	}
	if err := os.WriteFile(tokenPath, []byte(response.Msg.GetToken()+"\n"), 0o600); err != nil {
		return fmt.Errorf("write enrollment token for cluster %s: %w", clusterID, err)
	}
	fmt.Printf("enrollment token for cluster %s written to %s\n", clusterID, tokenPath)
	return nil
}

func ensureRoute(ctx context.Context, client orchestratorv1connect.OrchestratorServiceClient, token string, route routeSeed) error {
	req := connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
		Id: route.id, ClusterId: route.clusterID, ArtifactType: route.artifactType, Mode: route.mode,
		SourcePrefix: route.sourcePrefix, TargetPrefix: route.targetPrefix,
	})
	withAuth(req, token)
	response, err := client.ConfigureClusterRoute(ctx, req)
	if err != nil {
		return fmt.Errorf("configure route %s: %w", route.id, err)
	}
	if response.Msg.GetRoute().GetId() == route.id {
		fmt.Printf("route %s already configured\n", route.id)
	}
	return nil
}

func ensureDefinition(ctx context.Context, client orchestratorv1connect.OrchestratorServiceClient, token, customerID, clusterID string, index int) (string, error) {
	listReq := connect.NewRequest(&orchestratorv1.ListReleaseDefinitionsRequest{CustomerId: customerID, ClusterId: clusterID, IncludeDisabled: true})
	withAuth(listReq, token)
	list, err := client.ListReleaseDefinitions(ctx, listReq)
	if err != nil {
		return "", fmt.Errorf("list definitions for %s: %w", clusterID, err)
	}
	name := fmt.Sprintf("dev-release-%d", index+1)
	for _, definition := range list.Msg.GetDefinitions() {
		if definition.GetReleaseName() == name {
			fmt.Printf("release definition %s already exists\n", definition.GetId())
			return definition.GetId(), nil
		}
	}
	createReq := connect.NewRequest(&orchestratorv1.CreateReleaseDefinitionRequest{
		CustomerId: customerID, ClusterId: clusterID, Namespace: developmentNamespace,
		ReleaseName: name, ChartName: "release-manager", Enabled: true,
	})
	withAuth(createReq, token)
	response, err := client.CreateReleaseDefinition(ctx, createReq)
	if err != nil {
		return "", fmt.Errorf("create release definition for %s: %w", clusterID, err)
	}
	fmt.Printf("release definition %s created\n", response.Msg.GetDefinition().GetId())
	return response.Msg.GetDefinition().GetId(), nil
}

// ensureValuesRevision creates a draft values revision per definition (when
// none exists yet), then drives it to approved: Submit with the deployer
// actor, Approve with the platform admin actor (self-approval forbidden per
// REQ-068). Digest comparison uses the server-side digest returned by List
// (ADR-013: seed only uses public service seams; no local canonicalization).
func ensureValuesRevision(ctx context.Context, client orchestratorv1connect.OrchestratorServiceClient, deployerToken, adminToken, definitionID string, index int) error {
	valuesDocument := map[string]any{
		"environment": fmt.Sprintf("dev-%d", index+1),
		"replicas":    1,
		"service":     "release-manager",
	}
	valuesJSON, err := json.Marshal(valuesDocument)
	if err != nil {
		return fmt.Errorf("marshal seed values %s: %w", definitionID, err)
	}

	listReq := connect.NewRequest(&orchestratorv1.ListValuesRevisionsRequest{ReleaseDefinitionId: definitionID})
	withAuth(listReq, deployerToken)
	listResponse, err := client.ListValuesRevisions(ctx, listReq)
	if err != nil {
		return fmt.Errorf("list values revisions for %s: %w", definitionID, err)
	}
	for _, revision := range listResponse.Msg.GetRevisions() {
		if revision.GetStatus() == commonv1.ValuesStatus_VALUES_STATUS_APPROVED {
			fmt.Printf("values revision %s already approved\n", revision.GetId())
			return nil
		}
	}
	var pending *commonv1.ValuesRevision
	for _, revision := range listResponse.Msg.GetRevisions() {
		if revision.GetStatus() == commonv1.ValuesStatus_VALUES_STATUS_DRAFT ||
			revision.GetStatus() == commonv1.ValuesStatus_VALUES_STATUS_PENDING_APPROVAL {
			pending = revision
			break
		}
	}
	if pending == nil {
		createReq := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
			ReleaseDefinitionId: definitionID,
			Document:            valuesJSON,
		})
		createReq.Header().Set("Idempotency-Key", "devseed-create-values-"+definitionID)
		withAuth(createReq, deployerToken)
		created, err := client.CreateValuesRevision(ctx, createReq)
		if err != nil {
			return fmt.Errorf("create values revision for %s: %w", definitionID, err)
		}
		pending = created.Msg.GetRevision()
		fmt.Printf("values revision %s created\n", pending.GetId())
	}

	if pending.GetStatus() == commonv1.ValuesStatus_VALUES_STATUS_DRAFT {
		submitReq := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
			RevisionId: pending.GetId(), ExpectedStateVersion: pending.GetStateVersion(), Comment: "seeded by devseed",
		})
		withAuth(submitReq, deployerToken)
		submitted, err := client.SubmitValuesRevision(ctx, submitReq)
		if err != nil {
			return fmt.Errorf("submit values revision %s: %w", pending.GetId(), err)
		}
		pending = submitted.Msg.GetRevision()
		fmt.Printf("values revision %s submitted\n", pending.GetId())
	}

	approveReq := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{
		RevisionId: pending.GetId(), ExpectedStateVersion: pending.GetStateVersion(), Comment: "seeded by devseed",
	})
	withAuth(approveReq, adminToken)
	if _, err := client.ApproveValuesRevision(ctx, approveReq); err != nil {
		return fmt.Errorf("approve values revision %s: %w", pending.GetId(), err)
	}
	fmt.Printf("values revision %s approved\n", pending.GetId())
	return nil
}

func ensureReleaseBundle(ctx context.Context, client webhookv1connect.WebhookServiceClient) error {
	response, err := client.SubmitReleaseBundle(ctx, connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
		Name: "dev-release-bundle", ChartRef: "localhost:5001/release-manager-chart", ChartVersion: "0.1.0", ChartDigest: "sha256:dev-release-manager-chart",
		Images:    []*commonv1.BundleImage{{Ref: "localhost:5001/release-manager/api:dev", Digest: "sha256:dev-release-manager-api", ValuesPath: "image.repository"}},
		GitCommit: "dev", PipelineId: "dev-seed",
	}))
	if err != nil {
		return fmt.Errorf("submit release bundle: %w", err)
	}
	fmt.Printf("release bundle %s available\n", response.Msg.GetBundle().GetId())
	return nil
}
