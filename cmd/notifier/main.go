// Package main starts the release-notifier service.
package main

import (
	"flag"
	"log/slog"
	"net/http"

	"github.com/ndzuki/release-manager/internal/app"
)

type notifierSvc struct{}

func (s *notifierSvc) Name() string { return "release-notifier" }

func (s *notifierSvc) Register(_ *http.ServeMux, _ *slog.Logger) error { return nil }

func main() {
	configPath := flag.String("config", "configs/notifier.dev.yaml", "path to config file")
	flag.Parse()
	app.Run(*configPath, &notifierSvc{})
}
