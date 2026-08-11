// Package main starts the release-webhook service.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"

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
	svc := webhook.NewService(logger, client)
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
	flag.Parse()

	app.Run(*configPath, &webhookSvc{orchestratorURL: *orchestratorURL})
}
