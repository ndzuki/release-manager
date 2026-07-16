// Package main starts the release-webhook service.
package main

import (
	"flag"
	"log/slog"
	"net/http"

	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/webhook"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type webhookSvc2 struct {
	dbPath string
}

func (s *webhookSvc2) Name() string { return "release-webhook" }

func (s *webhookSvc2) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	svc := webhook.NewService(st, logger)
	path, h := webhookv1connect.NewWebhookServiceHandler(svc)
	mux.Handle(path, h)
	return nil
}

func main() {
	configPath := flag.String("config", "configs/webhook.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/webhook.db", "path to SQLite database")
	flag.Parse()

	app.Run(*configPath, &webhookSvc2{dbPath: *dbPath})
}
