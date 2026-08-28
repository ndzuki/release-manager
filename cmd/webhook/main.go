// Package main starts the release-webhook service.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"

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

func (s *webhookSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	url := s.orchestratorURL
	if url == "" {
		url = "http://localhost:8083"
	}
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

func (s *webhookSvc) Shutdown(_ context.Context) error { return nil }

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
