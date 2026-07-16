// Package main starts the release-webhook service.
package main

import (
	"flag"
	"log/slog"
	"net/http"

	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
	"github.com/ndzuki/release-manager/internal/webhook"
)

type webhookSvc struct {
	dbPath string
}

func (s *webhookSvc) Name() string { return "release-webhook" }

func (s *webhookSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	verifier := trust.NewStoreVerifier(
		trust.NewStubVerifier(st.Verifications(), logger),
		st.Verifications(),
		logger,
	)

	svc := webhook.NewService(st, verifier, logger)
	path, h := webhookv1connect.NewWebhookServiceHandler(svc)
	mux.Handle(path, h)
	return nil
}

func main() {
	configPath := flag.String("config", "configs/webhook.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/webhook.db", "path to SQLite database")
	flag.Parse()

	app.Run(*configPath, &webhookSvc{dbPath: *dbPath})
}
