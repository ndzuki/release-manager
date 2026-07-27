// Package main starts the release-orchestrator service.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/spf13/viper"
	"time"

	"connectrpc.com/connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/orchestrator"
	"github.com/ndzuki/release-manager/internal/orchestrator/operation"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
)

type orchSvc struct {
	dbPath           string
	targetEnv        string
	signingKey       string
	configPath       string
	operatorEndpoint string

	st      *sqlitestore.Store
	service *orchestrator.Service
	cleanup *orchestrator.CleanupService
}

func (s *orchSvc) Name() string { return "release-orchestrator" }

func (s *orchSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	s.st = st
	logger.Info("store opened", "db", s.dbPath)

	// Recover non-terminal operations from previous run (REQ-023 AC-023-05).
	recovered := operation.RecoverNonTerminal(context.Background(), st, logger, operation.DefaultRecoverOptions())
	if recovered > 0 {
		logger.Warn("operations recovered on restart", "count", recovered)
	}

	// Initialize audit emitter for async audit event persistence.
	auditCfg := audit.DefaultConfig()
	auditEmitter := audit.NewEmitter(st.AuditEvents(), logger, auditCfg)

	verifier := trust.NewStoreVerifier(
		trust.NewStubVerifier(st.Verifications(), nil, logger),
		st.Verifications(),
		logger,
	)

	streamRevoker := orchestrator.NewConnectOperatorStreamRevoker(http.DefaultClient, s.operatorEndpoint)
	svc := orchestrator.NewService(st, verifier, s.targetEnv, s.operatorEndpoint, auditEmitter, streamRevoker, logger)
	s.service = svc
	jwtMgr := auth.NewJWTManager([]byte(s.signingKey), 15*time.Minute, 7*24*time.Hour)
	enforcer, err := auth.NewEnforcer(st, logger)
	if err != nil {
		return fmt.Errorf("create orchestrator enforcer: %w", err)
	}
	if err := enforcer.LoadPolicies(context.Background()); err != nil {
		return fmt.Errorf("load orchestrator policies: %w", err)
	}
	interceptor := auth.NewAuthInterceptor(jwtMgr, st, enforcer, map[string]bool{}, logger)
	path, h := orchestratorv1connect.NewOrchestratorServiceHandler(
		svc,
		connect.WithInterceptors(interceptor),
	)
	mux.Handle(path, h)

	// Load retention config for GC.
	retCfg := orchestrator.DefaultRetentionConfig()
	if s.configPath != "" {
		v := viper.New()
		v.SetConfigFile(s.configPath)
		v.SetConfigType("yaml")
		if err := v.ReadInConfig(); err == nil {
			if err := v.UnmarshalKey("retention", &retCfg); err != nil {
				logger.Warn("failed to parse retention config, using defaults", "err", err)
			}
		}
	}
	if err := retCfg.Validate(); err != nil {
		logger.Error("invalid retention config", "err", err)
		return err
	}

	// Register CleanupService.
	s.cleanup = orchestrator.NewCleanupService(st, retCfg, logger)
	cpath, ch := orchestratorv1connect.NewCleanupServiceHandler(s.cleanup)
	mux.Handle(cpath, ch)
	logger.Info("cleanup service registered", "gc_interval_hours", retCfg.GCIntervalHours)

	return nil
}

// Run starts the GC ticker. Called by app.Run as a backgroundService.
func (s *orchSvc) Run(ctx context.Context) {
	if s.cleanup != nil {
		s.cleanup.StartTicker(ctx)
	}
}

func (s *orchSvc) Shutdown(ctx context.Context) error {
	if s.service == nil {
		return nil
	}
	return s.service.Shutdown(ctx)
}

// Close cleans up resources.
func (s *orchSvc) Close() error {
	if s.st != nil {
		return s.st.Close()
	}
	return nil
}

func main() {
	configPath := flag.String("config", "configs/orchestrator.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/release-manager.db", "path to the shared control-plane SQLite database")
	targetEnv := flag.String("target-env", "staging", "target environment (production, staging)")
	signingKey := flag.String("signing-key", "change-me-in-production", "JWT signing key")
	operatorEndpoint := flag.String("operator-endpoint", "http://localhost:8084", "Operator Connect endpoint exposed in enrollment instructions")
	flag.Parse()

	app.Run(*configPath, &orchSvc{dbPath: *dbPath, targetEnv: *targetEnv, configPath: *configPath, signingKey: *signingKey, operatorEndpoint: *operatorEndpoint})
}
