// Package main starts the release-webhook service.
package main

import (
	"flag"
	"log/slog"
	"net/http"

	"context"
	"errors"
	"fmt"
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/audit"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
	"github.com/ndzuki/release-manager/internal/webhook"
)

type webhookSvc struct {
	dbPath  string
	store   *sqlitestore.Store
	emitter *audit.Emitter
}

func (s *webhookSvc) Name() string { return "release-webhook" }

func (s *webhookSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	auditCfg := audit.DefaultConfig()
	auditCfg.SpoolPath = s.dbPath + ".audit-spool.jsonl"
	if _, err := audit.NewSpoolRecoverer(st.AuditEvents(), logger).Recover(context.Background(), auditCfg.SpoolPath); err != nil {
		return fmt.Errorf("recover audit spool: %w", err)
	}
	emitter := audit.NewEmitter(st.AuditEvents(), logger, auditCfg)
	s.store = st
	s.emitter = emitter
	verifier := trust.NewStoreVerifier(
		trust.NewStubVerifier(st.Verifications(), logger),
		st.Verifications(),
		logger,
	)
	svc := webhook.NewService(st, verifier, logger, emitter)
	path, h := webhookv1connect.NewWebhookServiceHandler(svc)
	mux.Handle(path, h)
	auditPath, auditHandler := auditv1connect.NewAuditServiceHandler(audit.NewService(emitter))
	mux.Handle(auditPath, auditHandler)
	return nil
}

func (s *webhookSvc) Shutdown(ctx context.Context) error {
	var errs []error
	if s.emitter != nil {
		errs = append(errs, s.emitter.Shutdown(ctx))
	}
	if s.store != nil {
		errs = append(errs, s.store.Close())
	}
	return errors.Join(errs...)
}

func main() {
	configPath := flag.String("config", "configs/webhook.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/webhook.db", "path to SQLite database")
	flag.Parse()

	app.Run(*configPath, &webhookSvc{dbPath: *dbPath})
}
