// Package main starts the release-auth service.
package main

import (
	"flag"
	"log/slog"
	"net/http"

	"github.com/ndzuki/release-manager/internal/app"
)

type authSvc struct{}

func (s *authSvc) Name() string { return "release-auth" }

func (s *authSvc) Register(_ *http.ServeMux, _ *slog.Logger) error { return nil }

func main() {
	configPath := flag.String("config", "configs/auth.dev.yaml", "path to config file")
	flag.Parse()
	app.Run(*configPath, &authSvc{})
}
