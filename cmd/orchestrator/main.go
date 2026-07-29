// Package main starts the release-orchestrator service.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/spf13/viper"

	"connectrpc.com/connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/orchestrator"
	"github.com/ndzuki/release-manager/internal/orchestrator/operation"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/internal/trust"
	"github.com/ndzuki/release-manager/migrations"
)

type orchSvc struct {
	cfg        config.ServiceConfig
	targetEnv  string
	signingKey string
	configPath string

	store      store.Store
	cleanup    *orchestrator.CleanupService
	pingDB     func(context.Context) error
	service    *orchestrator.Service
	bundleSvc  *orchestrator.BundleService
	validation *orchestrator.ValidationWorker
}

func (s *orchSvc) Name() string { return "release-orchestrator" }

func (s *orchSvc) Configure(cfg *config.ServiceConfig) { s.cfg = *cfg }

func (s *orchSvc) Close() error {
	if s.service != nil {
		s.service.Close()
	}
	if s.store == nil {
		return nil
	}
	return s.store.Close()
}
func (s *orchSvc) ReadinessChecks() map[string]func() error {
	if s.pingDB == nil {
		return nil
	}
	return map[string]func() error{
		"database": func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return s.pingDB(ctx)
		},
	}
}

func (s *orchSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	if err := s.openStore(); err != nil {
		return err
	}
	logger.Info("store opened", "driver", s.cfg.Database.Driver)
	if !s.cfg.Maintenance {
		recovered := operation.RecoverNonTerminal(context.Background(), s.store, logger, operation.DefaultRecoverOptions())
		if recovered > 0 {
			logger.Warn("operations recovered on restart", "count", recovered)
		}
	}

	verifier := trust.NewStoreVerifier(
		trust.NewStubVerifier(s.store.Verifications(), nil, logger),
		s.store.Verifications(),
		logger,
	)
	var auditEmitter audit.Sink
	if !s.cfg.Maintenance {
		auditEmitter = audit.NewEmitter(s.store.AuditEvents(), logger, audit.DefaultConfig())
	}
	s.service = orchestrator.NewService(s.store, verifier, s.targetEnv, auditEmitter, logger)
	svc := s.service
	if !s.cfg.Maintenance {
		if err := s.service.ResumePreflights(context.Background()); err != nil {
			return fmt.Errorf("resume preflight operations: %w", err)
		}
	}
	jwtMgr := auth.NewJWTManager([]byte(s.signingKey), 15*time.Minute, 7*24*time.Hour)
	enforcer, err := auth.NewEnforcer(s.store, logger)
	if err != nil {
		return fmt.Errorf("create orchestrator enforcer: %w", err)
	}
	if err := enforcer.LoadPolicies(context.Background()); err != nil {
		return fmt.Errorf("load orchestrator policies: %w", err)
	}

	path, handler := orchestratorv1connect.NewOrchestratorServiceHandler(
		svc,
		connect.WithInterceptors(
			app.MaintenanceInterceptor(s.cfg.Maintenance, orchestratorReadOnlyProcedures(), logger),
			auth.NewAuthInterceptor(jwtMgr, s.store, enforcer, map[string]bool{}, logger),
		),
	)
	mux.Handle(path, handler)

	retention, err := s.loadRetentionConfig()
	if err != nil {
		return err
	}
	s.cleanup = orchestrator.NewCleanupService(s.store, retention, logger)
	cleanupPath, cleanupHandler := orchestratorv1connect.NewCleanupServiceHandler(
		s.cleanup,
		connect.WithInterceptors(
			app.MaintenanceInterceptor(s.cfg.Maintenance, nil, logger),
		),
	)
	mux.Handle(cleanupPath, cleanupHandler)
	logger.Info("cleanup service registered", "gc_interval_hours", retention.GCIntervalHours)

	sourceRegistries := loadSourceRegistries(logger)
	bundleSvc := orchestrator.NewBundleService(s.store, logger, sourceRegistries)
	s.bundleSvc = bundleSvc
	bundlePath, bundleHandler := orchestratorv1connect.NewBundleServiceHandler(bundleSvc,
		connect.WithInterceptors(
			auth.TryAllInterceptor(logger,
				auth.NewAuthInterceptor(jwtMgr, s.store, enforcer, map[string]bool{}, logger),
				auth.ServiceTokenInterceptor(s.serviceTokens(), logger),
			),
		),
	)
	mux.Handle(bundlePath, bundleHandler)
	logger.Info("bundle service registered")

	if pgStore, ok := s.store.(*postgresstore.Store); ok {
		s.validation = orchestrator.NewValidationWorker(s.store, pgStore.GORM(), logger, orchestrator.DefaultValidationWorkerConfig())
	} else {
		s.validation = orchestrator.NewValidationWorker(s.store, nil, logger, orchestrator.DefaultValidationWorkerConfig())
	}

	return nil
}

func (s *orchSvc) openStore() error {
	if err := s.cfg.Database.Validate(); err != nil {
		return err
	}
	var err error
	switch s.cfg.Database.Driver {
	case "postgres":
		s.store, err = postgresstore.Open(context.Background(), s.cfg.Database, migrations.FS)
	case "sqlite":
		s.store, err = sqlitestore.Open(s.cfg.Database.DSN)
	}
	if err != nil {
		return err
	}
	switch backend := s.store.(type) {
	case *postgresstore.Store:
		s.pingDB = backend.SQLDB().PingContext
	case *sqlitestore.Store:
		s.pingDB = backend.DB().PingContext
	}
	return nil
}

func (s *orchSvc) loadRetentionConfig() (orchestrator.RetentionConfig, error) {
	retention := orchestrator.DefaultRetentionConfig()
	if s.configPath != "" {
		v := viper.New()
		v.SetConfigFile(s.configPath)
		v.SetConfigType("yaml")
		if err := v.ReadInConfig(); err == nil {
			if err := v.UnmarshalKey("retention", &retention); err != nil {
				return retention, fmt.Errorf("unmarshal retention config: %w", err)
			}
		}
	}
	if err := retention.Validate(); err != nil {
		return retention, fmt.Errorf("validate retention config: %w", err)
	}
	return retention, nil
}

func orchestratorReadOnlyProcedures() map[string]struct{} {
	return map[string]struct{}{
		orchestratorv1connect.OrchestratorServiceGetReleaseDefinitionProcedure:   {},
		orchestratorv1connect.OrchestratorServiceListReleaseDefinitionsProcedure: {},
		orchestratorv1connect.OrchestratorServiceGetCustomerProcedure:            {},
		orchestratorv1connect.OrchestratorServiceListCustomersProcedure:          {},
		orchestratorv1connect.OrchestratorServiceGetClusterProcedure:             {},
		orchestratorv1connect.OrchestratorServiceListClustersProcedure:           {},
		orchestratorv1connect.OrchestratorServiceGetClusterRoutesProcedure:       {},
		orchestratorv1connect.OrchestratorServiceGetOperationProcedure:           {},
	}
}

// Run starts the GC ticker. Called by app.Run as a backgroundService.
func (s *orchSvc) Run(ctx context.Context) {
	if s.validation != nil {
		go s.validation.Run(ctx)
	}
	if s.cfg.Maintenance || s.cleanup == nil {
		return
	}
	s.cleanup.StartTicker(ctx)
}

func (s *orchSvc) serviceTokens() []string { return nil }

func loadSourceRegistries(logger *slog.Logger) []orchestrator.SourceRegistry {
	_ = logger
	return nil
}

func main() {
	configPath := flag.String("config", "configs/orchestrator.dev.yaml", "path to config file")
	targetEnv := flag.String("target-env", "staging", "target environment (production, staging)")
	signingKey := flag.String("signing-key", "change-me-in-production", "JWT signing key")
	flag.Parse()

	app.Run(*configPath, &orchSvc{targetEnv: *targetEnv, configPath: *configPath, signingKey: *signingKey})
}
