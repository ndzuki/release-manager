// Package main starts the release-operator service.
package main

import (
	"flag"
	"log/slog"
	"net/http"

	"github.com/ndzuki/release-manager/internal/app"
)

type operatorSvc struct{}

func (s *operatorSvc) Name() string { return "release-operator" }

func (s *operatorSvc) Register(_ *http.ServeMux, _ *slog.Logger) error { return nil }

func main() {
	configPath := flag.String("config", "configs/operator.dev.yaml", "path to config file")
	flag.Parse()
	app.Run(*configPath, &operatorSvc{})
}
