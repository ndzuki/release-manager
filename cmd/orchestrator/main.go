// Package main starts the release-orchestrator service.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/orchestrator"
	"github.com/ndzuki/release-manager/internal/preflight"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
	"github.com/ndzuki/release-manager/internal/vulnerability"
)

type orchSvc struct {
	dbPath       string
	targetEnv    string
	signingKey   string
	auditEmitter *audit.Emitter
}

func (s *orchSvc) Name() string { return "release-orchestrator" }

func (s *orchSvc) Shutdown(ctx context.Context) error {
	if s.auditEmitter == nil {
		return nil
	}
	return s.auditEmitter.Shutdown(ctx)
}

func (s *orchSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	auditCfg := audit.DefaultConfig()
	s.auditEmitter = audit.NewEmitter(st.AuditEvents(), logger, auditCfg)

	jwtManager := auth.NewJWTManager([]byte(s.signingKey), 15*time.Minute, 7*24*time.Hour)
	enforcer, err := auth.NewEnforcer(st, logger)
	if err != nil {
		return err
	}
	if err := enforcer.LoadPolicies(context.Background()); err != nil {
		return err
	}
	interceptor := auth.NewAuthInterceptor(jwtManager, enforcer, map[string]bool{}, logger)

	verifier := trust.NewStoreVerifier(
		trust.NewStubVerifier(st.Verifications(), logger),
		st.Verifications(),
		logger,
	)

	runner := preflight.New(
		st.PreflightResults(),
		verifier,
		preflight.NewOCIResolver(http.DefaultClient),
		logger,
	)

	svc := orchestrator.NewService(
		st,
		verifier,
		runner,
		s.targetEnv,
		s.auditEmitter,
		logger,
	)

	// Wire vulnerability evaluator for SBOM-based admission.
	resultStore := &vulnerability.StoreResultStore{
		ScanStore:      st.ScanResults(),
		ExceptionStore: st.VulnerabilityExceptions(),
	}
	vulnEval := vulnerability.NewEvaluator(
		resultStore,
		vulnerability.NewNoopScanner("trivy"),
		vulnerability.DefaultProductionPolicy(),
	)
	svc.SetVulnerabilityEvaluator(vulnEval)
	path, h := orchestratorv1connect.NewOrchestratorServiceHandler(
		svc,
		connect.WithInterceptors(interceptor),
	)
	mux.Handle(path, h)
	return nil
}

func main() {
	configPath := flag.String("config", "configs/orchestrator.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/orchestrator.db", "path to SQLite database")
	targetEnv := flag.String("target-env", "staging", "target environment (production, staging)")
	signingKey := flag.String("signing-key", "change-me-in-production", "JWT signing key")
	flag.Parse()

	app.Run(*configPath, &orchSvc{dbPath: *dbPath, targetEnv: *targetEnv, signingKey: *signingKey})
}
