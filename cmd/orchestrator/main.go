// Package main starts the release-orchestrator service.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"

	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/orchestrator"
	"github.com/ndzuki/release-manager/internal/orchestrator/operation"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
)

type orchSvc struct {
	dbPath    string
	targetEnv string
}

func (s *orchSvc) Name() string { return "release-orchestrator" }

func (s *orchSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	// Recover non-terminal operations from previous run (REQ-023 AC-023-05).
	recovered := operation.RecoverNonTerminal(context.Background(), st, logger, operation.DefaultRecoverOptions())
	if recovered > 0 {
		logger.Warn("operations recovered on restart", "count", recovered)
	}

	// Initialize audit emitter for async audit event persistence.
	auditCfg := audit.DefaultConfig()
	auditEmitter := audit.NewEmitter(st.AuditEvents(), logger, auditCfg)
	_ = auditEmitter // Ready for injection into orchestrator handlers.

	// TODO: Inject auditEmitter into orchestrator.Service to record audit events
	// on CreateOperation, PublishRelease, and EmergencyChange.

	verifier := trust.NewStoreVerifier(
		trust.NewStubVerifier(st.Verifications(), logger),
		st.Verifications(),
		logger,
	)

	svc := orchestrator.NewService(st, verifier, s.targetEnv, logger)
	path, h := orchestratorv1connect.NewOrchestratorServiceHandler(svc)
	mux.Handle(path, h)
	return nil
}

func main() {
	configPath := flag.String("config", "configs/orchestrator.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/orchestrator.db", "path to SQLite database")
	targetEnv := flag.String("target-env", "staging", "target environment (production, staging)")
	flag.Parse()

	app.Run(*configPath, &orchSvc{dbPath: *dbPath, targetEnv: *targetEnv})
}
