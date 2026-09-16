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
		),
	)
	mux.Handle(path, handler)
	return nil
}

func main() {
	configPath := flag.String("config", "configs/webhook.dev.yaml", "path to config file")
	orchestratorURL := flag.String("orchestrator-url", "", "orchestrator Connect URL (default http://localhost:8083)")
	// REQ-065 批次3 D3: the dev lifecycle injects the bundle ingress service
	// token through the release-manager-webhook-service-token Secret as the
	// DEV_WEBHOOK_SERVICE_TOKEN env var; the flag default falls back to it.
	serviceToken := flag.String("service-token", envOr("DEV_WEBHOOK_SERVICE_TOKEN", ""), "bundle ingress service token forwarded to the orchestrator (env DEV_WEBHOOK_SERVICE_TOKEN)")
	flag.Parse()

	app.Run(*configPath, &webhookSvc{orchestratorURL: *orchestratorURL, serviceToken: *serviceToken})
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
