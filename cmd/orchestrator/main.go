// Package main starts the release-orchestrator service.
package main

import (
	"flag"
	"log/slog"
	"net/http"

	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/orchestrator"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type orchSvc struct {
	dbPath string
}

func (s *orchSvc) Name() string { return "release-orchestrator" }

func (s *orchSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	// Initialize audit emitter for async audit event persistence.
	auditCfg := audit.DefaultConfig()
	auditEmitter := audit.NewEmitter(st.AuditEvents(), logger, auditCfg)
	_ = auditEmitter // Ready for injection into orchestrator handlers.

	// TODO: Inject auditEmitter into orchestrator.Service to record audit events
	// on CreateOperation, PublishRelease, and EmergencyChange.

	svc := orchestrator.NewService(st, logger)
	path, h := orchestratorv1connect.NewOrchestratorServiceHandler(svc)
	mux.Handle(path, h)
	return nil
}

func main() {
	configPath := flag.String("config", "configs/orchestrator.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/orchestrator.db", "path to SQLite database")
	flag.Parse()

	app.Run(*configPath, &orchSvc{dbPath: *dbPath})
}
