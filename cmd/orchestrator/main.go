// Package main starts the release-orchestrator service.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/viper"

	"connectrpc.com/connect"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	trustv1connect "github.com/ndzuki/release-manager/api/gen/trust/v1/trustv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/config"
	contractsinterceptor "github.com/ndzuki/release-manager/internal/contracts/interceptor"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/operator/ca"
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
	authURL    string

	gateway       *http.Server
	store         store.Store
	cleanup       *orchestrator.CleanupService
	emergency     *orchestrator.Service
	pingDB        func(context.Context) error
	bundleSvc     *orchestrator.BundleService
	validation    *orchestrator.ValidationWorker
	auditEmitter  audit.Sink
	authorizer    *authorization.Module
	traceShutdown func(context.Context) error
	trustResolver trust.RootResolver

	// streamRegistry is the shared command-stream registry used to revoke live
	// Operator streams after a committed management write (REQ-053). It is
	// process-global by default; tests inject a private registry for isolation.
	streamRegistry *operator.StreamRegistry
}

func (s *orchSvc) Name() string { return "release-orchestrator" }

func (s *orchSvc) Configure(cfg *config.ServiceConfig) { s.cfg = *cfg }

// newGatewayOperatorService builds the OperatorService shared by the
// management plane and the agent gateway, and (when enabled) the mTLS gateway
// listener itself. The CA is persisted so the trust chain survives restarts
// (TASK-075 plan v1 Step 3).
func (s *orchSvc) newGatewayOperatorService(logger *slog.Logger) (*operator.Service, error) {
	gatewayCfg := s.cfg.Gateway.WithDefaults()
	gatewayOpts := []operator.Option{operator.WithAudit(s.auditEmitter), operator.WithStreamRegistry(s.operatorRegistry())}
	if gatewayCfg.Enabled {
		caInst, err := ca.LoadOrCreate(ca.Config{TTL: 7 * 24 * time.Hour}, gatewayCfg.CAKeyPath, gatewayCfg.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("load gateway CA: %w", err)
		}
		// The gateway service shares the persisted CA so Enroll signs
		// certificates from the same CA the listener verifies against.
		gatewayOpts = append(gatewayOpts, operator.WithCA(caInst))
		service, err := operator.NewService(s.store, logger, gatewayOpts...)
		if err != nil {
			return nil, fmt.Errorf("create gateway operator service: %w", err)
		}
		s.gateway, err = s.buildGatewayServer(gatewayCfg, caInst, service, logger)
		if err != nil {
			return nil, err
		}
		logger.Info("agent gateway enabled", "addr", s.gateway.Addr, "ca_cert", gatewayCfg.CACertPath)
		return service, nil
	}
	return operator.NewService(s.store, logger, gatewayOpts...)
}

// operatorRegistry returns the shared command-stream registry, defaulting to
// the process-wide registry for combined deployments (REQ-053).
func (s *orchSvc) operatorRegistry() *operator.StreamRegistry {
	if s.streamRegistry != nil {
		return s.streamRegistry
	}
	return operator.ProcessStreamRegistry()
}

// operatorEndpoint returns the agent-facing OperatorService URL embedded in
// enrollment install templates (REQ-053).
func (s *orchSvc) operatorEndpoint() string {
	gatewayCfg := s.cfg.Gateway.WithDefaults()
	if gatewayCfg.Enabled {
		return fmt.Sprintf("https://operator-gateway.dev.release-manager.local:%d", gatewayCfg.Port)
	}
	return "http://operator:8084"
}

// ExtraServers reports the agent gateway listener so app.Run starts and
// shuts it down together with the management-plane listener (TASK-075 plan
// v1 Step 2/3). The gateway is nil unless enabled by configuration. The
// method must be exported: app.Run type-asserts the exported
// internal/app.ExtraServersProvider interface, and unexported method names
// are package-scoped in Go — an unexported method here could never satisfy
// the cross-package interface (smoke-test catch, 2026-08-11).
func (s *orchSvc) ExtraServers() ([]*http.Server, error) {
	if s.gateway == nil {
		return nil, nil
	}
	return []*http.Server{s.gateway}, nil
}

// buildGatewayServer assembles the mTLS agent gateway listener (TASK-075 plan
// v1 Step 3): a CA-signed server certificate, client certificates verified
// when presented (VerifyClientCertIfGiven), and only the OperatorService
// handler mounted. Enroll accepts certificate-less requests; CommandStream
// enforces client certificates inside the handler (mixed mTLS contract).
func (s *orchSvc) buildGatewayServer(
	gatewayCfg config.GatewayCfg,
	caInst *ca.CA,
	operatorService *operator.Service,
	logger *slog.Logger,
) (*http.Server, error) {
	serverCertPEM, serverKeyPEM, err := caInst.SignServerCert([]string{
		"operator-gateway.dev.release-manager.local",
		"localhost",
	})
	if err != nil {
		return nil, fmt.Errorf("sign gateway server certificate: %w", err)
	}
	serverCert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("load gateway server certificate: %w", err)
	}

	gmux := http.NewServeMux()
	path, handler := operatorv1connect.NewOperatorServiceHandler(
		operatorService,
		connect.WithInterceptors(
			contractsinterceptor.NewRequestIDInterceptor(logger),
			contractsinterceptor.NewErrorSanitizeInterceptor(logger),
		),
	)
	gmux.Handle(path, gatewayTLSStateMiddleware(handler))

	return &http.Server{
		Addr:              fmt.Sprintf(":%d", gatewayCfg.Port),
		Handler:           gmux,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			ClientAuth:   tls.VerifyClientCertIfGiven,
			ClientCAs:    caInst.CertPool(),
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{"h2", "http/1.1"},
		},
	}, nil
}

// gatewayTLSStateMiddleware injects the TLS connection state of the gateway
// listener into the request context so CommandStream can enforce the mTLS
// identity path (TASK-075 plan v1 Step 3).
func gatewayTLSStateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			r = r.WithContext(operator.WithTLSState(r.Context(), r.TLS))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *orchSvc) Shutdown(ctx context.Context) error {
	var result error
	if emitter, ok := s.auditEmitter.(interface{ Shutdown(context.Context) error }); ok {
		result = emitter.Shutdown(ctx)
	}
	if s.traceShutdown != nil {
		if err := s.traceShutdown(ctx); result == nil {
			result = err
		}
	}
	return result
}

func (s *orchSvc) Close() error {
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
	metrics := authorization.NewMetrics(prometheus.NewRegistry())
	mux.Handle("GET /metrics", metrics.Handler())
	s.traceShutdown = authorization.InstallTracing()
	authzConfig := s.cfg.Authorization.WithDefaults()
	if s.authURL != "" {
		authzConfig.AuthURL = s.authURL
	}
	authClient := authv1connect.NewAuthorizationServiceClient(
		http.DefaultClient,
		authzConfig.AuthURL,
		connect.WithInterceptors(authorization.TraceInterceptor()),
	)
	s.authorizer = authorization.NewModule(
		authClient, s.store.Authorization(), metrics, logger,
		authzConfig.PullInterval, authzConfig.PullBackoffMax,
	)
	if !s.cfg.Maintenance {
		recovered := operation.RecoverNonTerminal(context.Background(), s.store, logger, operation.DefaultRecoverOptions())
		if recovered > 0 {
			logger.Warn("operations recovered on restart", "count", recovered)
		}
	}

	if !s.cfg.Maintenance {
		s.auditEmitter = audit.NewEmitter(s.store.AuditEvents(), logger, audit.DefaultConfig())
	}
	trustConfig, err := s.loadTrustConfig()
	if err != nil {
		return err
	}
	resolver := s.trustResolver
	if resolver == nil {
		resolver = trust.NewStoreResolver(s.store.TrustRoots())
	}
	verifier := trust.NewEd25519Verifier(
		s.store.Verifications(),
		resolver,
		trustConfig.VerificationTimeout,
		logger,
	)
	// Agent gateway (TASK-075 plan v1 Step 3): a second TLS listener serving
	// only the OperatorService handler for customer cluster agents. A
	// persisted CA keeps the trust chain stable across restarts; the service
	// shares it via WithCA so Enroll signs certificates from the same CA the
	// listener verifies against.
	operatorService, err := s.newGatewayOperatorService(logger)
	if err != nil {
		return fmt.Errorf("create operator control service: %w", err)
	}
	operatorPath, operatorHandler := operatorv1connect.NewOperatorServiceHandler(
		operatorService,
		connect.WithInterceptors(
			contractsinterceptor.NewRequestIDInterceptor(logger),
			contractsinterceptor.NewErrorSanitizeInterceptor(logger),
		),
	)
	mux.Handle(operatorPath, operatorHandler)
	emergencyDispatcher := orchestrator.NewEmergencyDispatcher(operatorService)
	var createOperation orchestrator.OperationCreationUnitOfWork
	if uowProvider, ok := s.store.(interface {
		OperationCreationUnitOfWork() store.OperationCreationUnitOfWork
	}); ok {
		createOperation = uowProvider.OperationCreationUnitOfWork()
	}
	svc := orchestrator.NewService(s.store, verifier, s.targetEnv, s.auditEmitter, emergencyDispatcher, orchestrator.NewProcessStreamRevoker(s.operatorRegistry()), s.operatorEndpoint(), createOperation, s.authorizer, logger)
	s.emergency = svc
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
			contractsinterceptor.NewRequestIDInterceptor(logger),
			contractsinterceptor.NewErrorSanitizeInterceptor(logger),
			authorization.TraceInterceptor(),
			app.MaintenanceInterceptor(s.cfg.Maintenance, orchestratorReadOnlyProcedures(), logger),
			auth.NewAuthInterceptor(jwtMgr, s.store, enforcer, map[string]bool{}, logger),
			auth.NewAuthStreamInterceptor(jwtMgr, s.store, enforcer, map[string]bool{}, logger),
		),
	)
	mux.Handle(path, handler)
	s.registerTrustService(mux, logger, jwtMgr, enforcer, trustConfig)

	retention, err := s.loadRetentionConfig()
	if err != nil {
		return err
	}
	s.cleanup = orchestrator.NewCleanupService(s.store, retention, logger)
	cleanupPath, cleanupHandler := orchestratorv1connect.NewCleanupServiceHandler(
		s.cleanup,
		connect.WithInterceptors(
			contractsinterceptor.NewRequestIDInterceptor(logger),
			contractsinterceptor.NewErrorSanitizeInterceptor(logger),
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
			contractsinterceptor.NewRequestIDInterceptor(logger),
			contractsinterceptor.NewErrorSanitizeInterceptor(logger),
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

func (s *orchSvc) registerTrustService(
	mux *http.ServeMux,
	logger *slog.Logger,
	jwtMgr *auth.JWTManager,
	enforcer *auth.Enforcer,
	trustConfig trustConfig,
) {
	trustService := trust.NewTrustService(s.store.TrustRoots(), s.auditEmitter, logger)
	trustPath, trustHandler := trustv1connect.NewTrustServiceHandler(
		trustService,
		connect.WithInterceptors(
			authorization.TraceInterceptor(),
			app.MaintenanceInterceptor(s.cfg.Maintenance, trustReadOnlyProcedures(), logger),
			auth.NewAuthInterceptor(jwtMgr, s.store, enforcer, map[string]bool{}, logger),
		),
	)
	mux.Handle(trustPath, trustHandler)
	logger.Info("trust service registered", "verification_timeout", trustConfig.VerificationTimeout)
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

type trustConfig struct {
	VerificationTimeout time.Duration `mapstructure:"verification_timeout"`
}

func defaultTrustConfig() trustConfig {
	return trustConfig{VerificationTimeout: trust.DefaultVerificationTimeout}
}

func (s *orchSvc) loadTrustConfig() (trustConfig, error) {
	trustCfg := defaultTrustConfig()
	if s.configPath != "" {
		v := viper.New()
		v.SetConfigFile(s.configPath)
		v.SetConfigType("yaml")
		if err := v.ReadInConfig(); err == nil {
			if err := v.UnmarshalKey("trust", &trustCfg); err != nil {
				return trustCfg, fmt.Errorf("unmarshal trust config: %w", err)
			}
		}
	}
	if trustCfg.VerificationTimeout <= 0 {
		trustCfg.VerificationTimeout = trust.DefaultVerificationTimeout
	}
	return trustCfg, nil
}

func orchestratorReadOnlyProcedures() map[string]struct{} {
	return map[string]struct{}{
		orchestratorv1connect.OrchestratorServiceGetReleaseDefinitionProcedure:     {},
		orchestratorv1connect.OrchestratorServiceListReleaseDefinitionsProcedure:   {},
		orchestratorv1connect.OrchestratorServiceGetCustomerProcedure:              {},
		orchestratorv1connect.OrchestratorServiceListCustomersProcedure:            {},
		orchestratorv1connect.OrchestratorServiceGetClusterProcedure:               {},
		orchestratorv1connect.OrchestratorServiceListClustersProcedure:             {},
		orchestratorv1connect.OrchestratorServiceGetClusterRoutesProcedure:         {},
		orchestratorv1connect.OrchestratorServiceGetOperationProcedure:             {},
		orchestratorv1connect.OrchestratorServiceListOperatorsProcedure:            {},
		orchestratorv1connect.OrchestratorServiceGetOperatorProcedure:              {},
		orchestratorv1connect.OrchestratorServiceGetEnrollmentTokenStatusProcedure: {},
	}
}

func trustReadOnlyProcedures() map[string]struct{} {
	return map[string]struct{}{
		trustv1connect.TrustServiceGetTrustPolicyProcedure: {},
	}
}

// Run starts background cleanup and emergency timeout scanning.
func (s *orchSvc) Run(ctx context.Context) {
	if s.authorizer != nil {
		go s.authorizer.Run(ctx)
	}
	if s.validation != nil {
		go s.validation.Run(ctx)
	}
	if s.cfg.Maintenance || s.cleanup == nil {
		return
	}
	if s.cleanup != nil {
		go s.cleanup.StartTicker(ctx)
	}
	if s.emergency == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.emergency.ExpireEmergencyOperations(ctx)
		}
	}
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
