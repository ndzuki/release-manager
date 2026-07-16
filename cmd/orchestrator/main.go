// Package main starts the release-orchestrator service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"

	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/orchestrator"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
)

type orchSvc struct {
	dbPath    string
	targetEnv string
	store     *sqlitestore.Store
	emitter   *audit.Emitter
}

func (s *orchSvc) Name() string { return "release-orchestrator" }

func (s *orchSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
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

	svc := orchestrator.NewService(st, verifier, s.targetEnv, logger, emitter)
	path, h := orchestratorv1connect.NewOrchestratorServiceHandler(svc)
	mux.Handle(path, h)
	auditPath, auditHandler := auditv1connect.NewAuditServiceHandler(audit.NewService(emitter))
	mux.Handle(auditPath, auditHandler)
	return nil
}

func (s *orchSvc) Shutdown(ctx context.Context) error {
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
	configPath := flag.String("config", "configs/orchestrator.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/orchestrator.db", "path to SQLite database")
	targetEnv := flag.String("target-env", "staging", "target environment (production, staging)")
	flag.Parse()

	app.Run(*configPath, &orchSvc{dbPath: *dbPath, targetEnv: *targetEnv})
}
