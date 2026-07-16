// Package main starts the release-webhook service.
package main

import (
	"flag"
	"log/slog"
	"net/http"

	"github.com/ndzuki/release-manager/internal/app"
)

type webhookSvc struct{}

func (s *webhookSvc) Name() string { return "release-webhook" }

func (s *webhookSvc) Register(_ *http.ServeMux, _ *slog.Logger) error { return nil }

func main() {
	configPath := flag.String("config", "configs/webhook.dev.yaml", "path to config file")
	flag.Parse()
	app.Run(*configPath, &webhookSvc{})
}
