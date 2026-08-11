// Package main seeds deterministic development data through public service seams.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	webhookv1 "github.com/ndzuki/release-manager/api/gen/webhook/v1"
	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
	"github.com/ndzuki/release-manager/internal/values"
)

const (
	defaultOrchestratorURL = "http://localhost:8083"
	defaultWebhookURL      = "http://localhost:8082"
	defaultValuesURL       = "http://localhost:8087"
	developmentNamespace   = "release-manager-dev"
	// devEnrollmentTokenDir holds one 0600 enrollment token file per dev
	// cluster (TASK-075 plan v1 Step 8); TASK-065 replaces this with Secret
	// injection into the customer clusters.
	devEnrollmentTokenDir = "data/dev-enrollment-tokens" //nolint:gosec // directory path, not a credential
)

type seedConfig struct {
	orchestratorURL string
	webhookURL      string
	valuesURL       string
}

type valuesRevision struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Digest string `json:"digest"`
}

type valuesListResponse struct {
	Revisions []valuesRevision `json:"revisions"`
}

type valuesCreateResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
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
	flag.StringVar(&cfg.valuesURL, "values", defaultValuesURL, "Values API URL")
	flag.Parse()

	if err := run(context.Background(), cfg); err != nil {
		slog.Error("development seed failed", "error", err)
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
	fmt.Println("development seed complete")
	fmt.Printf("namespace: %s\n", developmentNamespace)
	fmt.Printf("orchestrator: %s\n", strings.TrimRight(cfg.orchestratorURL, "/"))
	fmt.Printf("webhook: %s\n", strings.TrimRight(cfg.webhookURL, "/"))
	fmt.Printf("values API: %s\n", strings.TrimRight(cfg.valuesURL, "/"))
}

func run(ctx context.Context, cfg seedConfig) error {
	httpClient := http.DefaultClient
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
		if err := ensureCustomer(ctx, orchestratorClient, customer.id, customer.name, customer.slug); err != nil {
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
		if err := ensureCluster(ctx, orchestratorClient, cluster.id, cluster.name, cluster.customerID); err != nil {
			return err
		}
	}
	if err := ensureEnrollmentTokens(ctx, orchestratorClient, clusters, devEnrollmentTokenDir); err != nil {
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
		if err := ensureRoute(ctx, orchestratorClient, route); err != nil {
			return err
		}
	}

	definitions := make([]string, 0, len(clusters))
	for i, cluster := range clusters {
		definitionID, err := ensureDefinition(ctx, orchestratorClient, cluster.customerID, cluster.id, i)
		if err != nil {
			return err
		}
		definitions = append(definitions, definitionID)
	}

	for i, definitionID := range definitions {
		if err := ensureValuesRevision(ctx, httpClient, cfg.valuesURL, definitionID, i); err != nil {
			return err
		}
	}

	if err := ensureReleaseBundle(ctx, webhookClient); err != nil {
		return err
	}
	return nil
}

func ensureCustomer(ctx context.Context, client orchestratorv1connect.OrchestratorServiceClient, id, name, slug string) error {
	_, err := client.GetCustomer(ctx, connect.NewRequest(&orchestratorv1.GetCustomerRequest{CustomerId: id}))
	if err == nil {
		fmt.Printf("customer %s already exists\n", id)
		return nil
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		return fmt.Errorf("get customer %s: %w", id, err)
	}
	_, err = client.CreateCustomer(ctx, connect.NewRequest(&orchestratorv1.CreateCustomerRequest{Id: id, Name: name, Slug: slug}))
	if err != nil {
		return fmt.Errorf("create customer %s: %w", id, err)
	}
	fmt.Printf("customer %s created\n", id)
	return nil
}

func ensureCluster(ctx context.Context, client orchestratorv1connect.OrchestratorServiceClient, id, name, customerID string) error {
	_, err := client.GetCluster(ctx, connect.NewRequest(&orchestratorv1.GetClusterRequest{ClusterId: id}))
	if err == nil {
		fmt.Printf("cluster %s already exists\n", id)
		return nil
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		return fmt.Errorf("get cluster %s: %w", id, err)
	}
	_, err = client.CreateCluster(ctx, connect.NewRequest(&orchestratorv1.CreateClusterRequest{Id: id, Name: name, CustomerId: customerID, KubeconfigRef: "kind://release-manager-dev"}))
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
func ensureEnrollmentTokens(ctx context.Context, client enrollmentTokenCreator, clusters []clusterSeed, tokenDir string) error {
	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		return fmt.Errorf("create enrollment token dir: %w", err)
	}
	for _, cluster := range clusters {
		if err := ensureEnrollmentToken(ctx, client, cluster.id, cluster.customerID, tokenDir); err != nil {
			return err
		}
	}
	return nil
}

// ensureEnrollmentToken writes the enrollment token file for one cluster.
// The token value appears only in the CreateEnrollmentToken response and is
// never logged (REQ-065 log safety).
func ensureEnrollmentToken(ctx context.Context, client enrollmentTokenCreator, clusterID, customerID, tokenDir string) error {
	tokenPath := filepath.Join(tokenDir, clusterID+".token")
	if _, err := os.Stat(tokenPath); err == nil {
		fmt.Printf("enrollment token for cluster %s already seeded\n", clusterID)
		return nil
	}
	response, err := client.CreateEnrollmentToken(ctx, connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
		CustomerId: customerID,
		ClusterId:  clusterID,
	}))
	if err != nil {
		return fmt.Errorf("create enrollment token for cluster %s: %w", clusterID, err)
	}
	if err := os.WriteFile(tokenPath, []byte(response.Msg.GetToken()+"\n"), 0o600); err != nil {
		return fmt.Errorf("write enrollment token for cluster %s: %w", clusterID, err)
	}
	fmt.Printf("enrollment token for cluster %s written to %s\n", clusterID, tokenPath)
	return nil
}

func ensureRoute(ctx context.Context, client orchestratorv1connect.OrchestratorServiceClient, route routeSeed) error {
	response, err := client.ConfigureClusterRoute(ctx, connect.NewRequest(&orchestratorv1.ConfigureClusterRouteRequest{
		Id: route.id, ClusterId: route.clusterID, ArtifactType: route.artifactType, Mode: route.mode,
		SourcePrefix: route.sourcePrefix, TargetPrefix: route.targetPrefix,
	}))
	if err != nil {
		return fmt.Errorf("configure route %s: %w", route.id, err)
	}
	if response.Msg.GetRoute().GetId() == route.id {
		fmt.Printf("route %s already configured\n", route.id)
	}
	return nil
}

func ensureDefinition(ctx context.Context, client orchestratorv1connect.OrchestratorServiceClient, customerID, clusterID string, index int) (string, error) {
	list, err := client.ListReleaseDefinitions(ctx, connect.NewRequest(&orchestratorv1.ListReleaseDefinitionsRequest{CustomerId: customerID, ClusterId: clusterID, IncludeDisabled: true}))
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
	response, err := client.CreateReleaseDefinition(ctx, connect.NewRequest(&orchestratorv1.CreateReleaseDefinitionRequest{
		CustomerId: customerID, ClusterId: clusterID, Namespace: developmentNamespace,
		ReleaseName: name, ChartName: "release-manager", Enabled: true,
	}))
	if err != nil {
		return "", fmt.Errorf("create release definition for %s: %w", clusterID, err)
	}
	fmt.Printf("release definition %s created\n", response.Msg.GetDefinition().GetId())
	return response.Msg.GetDefinition().GetId(), nil
}

func ensureValuesRevision(ctx context.Context, client *http.Client, baseURL, definitionID string, index int) error {
	valuesDocument := map[string]any{
		"environment": fmt.Sprintf("dev-%d", index+1),
		"replicas":    1,
		"service":     "release-manager",
	}
	valuesJSON, err := json.Marshal(valuesDocument)
	if err != nil {
		return fmt.Errorf("marshal seed values %s: %w", definitionID, err)
	}
	canonical, err := values.Validate(valuesJSON, 1<<20)
	if err != nil {
		return fmt.Errorf("validate seed values %s: %w", definitionID, err)
	}

	url := strings.TrimRight(baseURL, "/") + "/api/v1/values-revisions?definition_id=" + definitionID
	listBody, err := doJSON(ctx, client, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("list values revisions for %s: %w", definitionID, err)
	}
	var listed valuesListResponse
	if err := json.Unmarshal(listBody, &listed); err != nil {
		return fmt.Errorf("decode values revisions for %s: %w", definitionID, err)
	}
	for _, revision := range listed.Revisions {
		if revision.Digest != canonical.Digest {
			continue
		}
		if revision.Status == "approved" {
			fmt.Printf("values revision %s already approved\n", revision.ID)
			return nil
		}
		return approveValuesRevision(ctx, client, baseURL, revision.ID)
	}

	payload := map[string]any{"release_definition_id": definitionID, "values": string(valuesJSON)}
	createdBody, err := doJSON(ctx, client, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v1/values-revisions", payload)
	if err != nil {
		return fmt.Errorf("create values revision for %s: %w", definitionID, err)
	}
	var created valuesCreateResponse
	if err := json.Unmarshal(createdBody, &created); err != nil {
		return fmt.Errorf("decode created values revision for %s: %w", definitionID, err)
	}
	fmt.Printf("values revision %s created\n", created.ID)
	return approveValuesRevision(ctx, client, baseURL, created.ID)
}

func approveValuesRevision(ctx context.Context, client *http.Client, baseURL, revisionID string) error {
	_, err := doJSON(ctx, client, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v1/values-revisions/"+revisionID+"/approve", nil)
	if err != nil {
		return fmt.Errorf("approve values revision %s: %w", revisionID, err)
	}
	fmt.Printf("values revision %s approved\n", revisionID)
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

func doJSON(ctx context.Context, client *http.Client, method, url string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode JSON: %w", err)
		}
		body = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create HTTP request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send HTTP request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read HTTP response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	return responseBody, nil
}
