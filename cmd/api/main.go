// Package main starts the release-api service.
package main

import (
	"flag"
	"log/slog"
	"net/http"

	"github.com/ndzuki/release-manager/internal/app"
)

type apiSvc struct{}

func (s *apiSvc) Name() string { return "release-api" }

func (s *apiSvc) Register(_ *http.ServeMux, _ *slog.Logger) error { return nil }

func main() {
	configPath := flag.String("config", "configs/api.dev.yaml", "path to config file")
	flag.Parse()
	app.Run(*configPath, &apiSvc{})
}
