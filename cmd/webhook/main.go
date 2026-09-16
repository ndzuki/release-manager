// Package main starts the release-webhook service.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/config"
	contractsinterceptor "github.com/ndzuki/release-manager/internal/contracts/interceptor"
	"github.com/ndzuki/release-manager/internal/webhook"
)

type webhookSvc struct {
	orchestratorURL string
	// serviceToken is the dev bundle-ingress service token forwarded to the
	// orchestrator BundleService (REQ-011 §562 dev minimal wiring, D-100
	// 选项 B, AC-065-33); empty in production/non-dev runs.
	serviceToken string
	// ciAPIKeyHashes guard the inbound CI entrypoint: only the CI key may call
	// WebhookService/SubmitReleaseBundle (REQ-011 §562, AC-011-16/17).
	ciAPIKeyHashes []string
	// harborAPIKeyHashes guard POST /webhooks/harbor.
	harborAPIKeyHashes []string
	// harborServiceToken is forwarded to the orchestrator as the Harbor service
	// identity; it only authorizes RecordArtifactEvent.
	harborServiceToken string
	// harborSourceID is the immutable source id recorded with each Harbor event.
	harborSourceID string
}

func (s *webhookSvc) Name() string { return "release-webhook" }

func (s *webhookSvc) Configure(_ *config.ServiceConfig) {}

func (s *webhookSvc) Shutdown(_ context.Context) error { return nil }

func (s *webhookSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	url := s.orchestratorBaseURL()
	client := orchestratorv1connect.NewBundleServiceClient(
		http.DefaultClient,
		url,
		connect.WithGRPC(),
	)
	svc := webhook.NewService(logger, client, s.serviceToken)
	path, handler := webhookv1connect.NewWebhookServiceHandler(
		svc,
		connect.WithInterceptors(
			contractsinterceptor.NewRequestIDInterceptor(logger),
			contractsinterceptor.NewErrorSanitizeInterceptor(logger),
			// REQ-011 §562: the CI key is scoped to SubmitReleaseBundle and cannot
			// reach any other procedure (AC-011-16/17).
			auth.ServiceTokenInterceptor("release-ci", s.ciAPIKeyHashes, logger,
				webhookv1connect.WebhookServiceSubmitReleaseBundleProcedure),
		),
	)
	mux.Handle(path, handler)

	// Harbor artifact ingress (REQ-011 §562): plain CloudEvents HTTP, guarded by
	// the separate Harbor key, forwarded as RecordArtifactEvent.
	sourceID := s.harborSourceID
	if sourceID == "" {
		sourceID = "harbor"
	}
	harborHashes := s.harborAPIKeyHashes
	mux.Handle("POST "+harborPath, webhook.RequireToken(
		func(token string) bool { return auth.VerifyTokenHash(token, harborHashes) },
		webhook.NewHarborHandler(client, sourceID, s.harborServiceToken),
	))
	logger.Info("harbor ingress registered", "path", harborPath, "source_id", sourceID)
	return nil
}

// harborPath is the Harbor CloudEvents ingress path (REQ-011 §562).
const harborPath = "/webhooks/harbor"

func main() {
	configPath := flag.String("config", "configs/webhook.dev.yaml", "path to config file")
	orchestratorURL := flag.String("orchestrator-url", "", "orchestrator Connect URL (default http://localhost:8083)")
	// REQ-065 批次3 D3: the dev lifecycle injects the bundle ingress service
	// token through the release-manager-webhook-service-token Secret as the
	// DEV_WEBHOOK_SERVICE_TOKEN env var; the flag default falls back to it.
	serviceToken := flag.String("service-token", envOr("DEV_WEBHOOK_SERVICE_TOKEN", ""), "bundle ingress service token forwarded to the orchestrator (env DEV_WEBHOOK_SERVICE_TOKEN)")
	// REQ-011 §562: two independent, rotatable keys. The CI key only authorizes
	// SubmitReleaseBundle; the Harbor key only authorizes POST /webhooks/harbor
	// and RecordArtifactEvent. Both accept a previous value for zero-downtime
	// rotation (AC-011-04: the two keys cannot be substituted for each other).
	ciAPIKey := flag.String("ci-api-key", envOr("DEV_CI_API_KEY", ""), "CI API key accepted by SubmitReleaseBundle (env DEV_CI_API_KEY)")
	ciAPIKeyPrevious := flag.String("ci-api-key-previous", envOr("DEV_CI_API_KEY_PREVIOUS", ""), "previous CI API key during rotation (env DEV_CI_API_KEY_PREVIOUS)")
	harborAPIKey := flag.String("harbor-api-key", envOr("DEV_HARBOR_API_KEY", ""), "Harbor API key accepted by POST /webhooks/harbor (env DEV_HARBOR_API_KEY)")
	harborAPIKeyPrevious := flag.String("harbor-api-key-previous", envOr("DEV_HARBOR_API_KEY_PREVIOUS", ""), "previous Harbor API key during rotation (env DEV_HARBOR_API_KEY_PREVIOUS)")
	harborServiceToken := flag.String("harbor-service-token", envOr("DEV_HARBOR_SERVICE_TOKEN", envOr("DEV_HARBOR_API_KEY", "")), "service token forwarded to orchestrator RecordArtifactEvent (env DEV_HARBOR_SERVICE_TOKEN)")
	harborSourceID := flag.String("harbor-source-id", envOr("DEV_HARBOR_SOURCE_ID", "harbor"), "immutable source id recorded with Harbor events")
	flag.Parse()

	app.Run(*configPath, &webhookSvc{
		orchestratorURL:    *orchestratorURL,
		serviceToken:       *serviceToken,
		ciAPIKeyHashes:     auth.TokenHashes(*ciAPIKey, *ciAPIKeyPrevious),
		harborAPIKeyHashes: auth.TokenHashes(*harborAPIKey, *harborAPIKeyPrevious),
		harborServiceToken: *harborServiceToken,
		harborSourceID:     *harborSourceID,
	})
}

// envOr returns the environment value or the fallback when unset.
func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// orchestratorBaseURL is the single source of truth for the upstream
// orchestrator endpoint, shared by Register's BundleService client and the
// /readyz probe (TASK-099).
func (s *webhookSvc) orchestratorBaseURL() string {
	if s.orchestratorURL != "" {
		return s.orchestratorURL
	}
	return "http://localhost:8083"
}

// ReadinessChecks implements app's readinessContributor (TASK-099 AC3): the
// webhook only exists to forward bundle-ingress traffic to the orchestrator,
// so "Ready" must mean that upstream answers its own /readyz with 200 — not
// the previous vacuous noop.
func (s *webhookSvc) ReadinessChecks() map[string]func() error {
	base := s.orchestratorBaseURL()
	return map[string]func() error{
		"orchestrator": func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/readyz", http.NoBody)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("orchestrator %s not reachable: %w", base, err)
			}
			defer resp.Body.Close() //nolint:errcheck // the status code is the signal; draining the body is not
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("orchestrator %s/readyz answered %d (not ready)", base, resp.StatusCode)
			}
			return nil
		},
	}
}
