// Package main starts the release-orchestrator service.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
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
	enforcer      *auth.Enforcer
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
// listener itself. The CA is loaded from the configured source (Vault KV or
// dev files, ADR-017) so the trust chain survives restarts; missing or
// unavailable credentials fail closed.
func (s *orchSvc) newGatewayOperatorService(logger *slog.Logger) (*operator.Service, error) {
	gatewayCfg := s.cfg.Gateway.WithDefaults()
	caInst, renewRatio, err := ca.LoadConfigured(context.Background(), s.cfg.CA)
	if err != nil {
		return nil, fmt.Errorf("load operator CA: %w", err)
	}
	gatewayOpts := []operator.Option{
		operator.WithCA(caInst),
		operator.WithRenewBeforeRatio(renewRatio),
		operator.WithAudit(s.auditEmitter),
		operator.WithStreamRegistry(s.operatorRegistry()),
	}
	if gatewayCfg.Enabled {
		// The gateway service shares the persisted CA so Enroll signs
		// certificates from the same CA the listener verifies against.
		service, err := operator.NewService(s.store, logger, gatewayOpts...)
		if err != nil {
			return nil, fmt.Errorf("create gateway operator service: %w", err)
		}
		s.gateway, err = s.buildGatewayServer(gatewayCfg, caInst, service, logger)
		if err != nil {
			return nil, err
		}
		logger.Info("agent gateway enabled", "addr", s.gateway.Addr, "ca_cert", s.cfg.CA.CertPath)
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
	gmux.Handle(path, operator.NewCertificateIdentityHandler(gatewayTLSStateMiddleware(handler)))

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
	if s.emergency != nil {
		// Drain preflight coordinators before the Store closes so no runner
		// touches a closed database (TASK-019 v3 Step 5).
		if err := s.emergency.Shutdown(ctx); result == nil {
			result = err
		}
	}
	if emitter, ok := s.auditEmitter.(interface{ Shutdown(context.Context) error }); ok {
		if err := emitter.Shutdown(ctx); result == nil {
			result = err
		}
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
	checks := map[string]func() error{}
	if s.pingDB != nil {
		checks["database"] = func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return s.pingDB(ctx)
		}
	}
	if s.cleanup != nil {
		checks["cleanup_gc"] = func() error {
			if !s.cleanup.Health() {
				return fmt.Errorf("cleanup gc unhealthy")
			}
			return nil
		}
	}
	return checks
}

// GCHealthJSON renders the REQ-069 gc sub-object for /health: status plus the
// last success/attempt timestamps as Unix seconds (AC-069-32/33/34).
func (s *orchSvc) GCHealthJSON() any {
	if s.cleanup == nil {
		return map[string]any{"status": "healthy"}
	}
	snapshot := s.cleanup.GCHealthSnapshot()
	if snapshot.Disabled {
		snapshot.Status = orchestrator.GCHealthHealthy
	}
	unix := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.Unix()
	}
	return map[string]any{
		"status":          string(snapshot.Status),
		"last_success_at": unix(snapshot.LastSuccess),
		"last_attempt_at": unix(snapshot.LastAttempt),
	}
}

// Register wires every service onto the shared mux; each block is a
// self-contained registration branch.
//
//nolint:gocyclo // Service registration is a flat list of independent branches.
func (s *orchSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	if err := s.openStore(); err != nil {
		return err
	}
	logger.Info("store opened", "driver", s.cfg.Database.Driver)
	// REQ-081 D2=A: the orchestrator is the production writer of the
	// emergency kill switch configuration — load emergency.* from the config
	// and upsert it into app_settings before serving requests. A missing
	// section fails closed (enabled=false, default timeout) and preserves an
	// existing app_settings value.
	emergencyCfg, emergencyPresent, err := s.loadEmergencyConfig()
	if err != nil {
		return err
	}
	if err := s.seedEmergencyConfig(context.Background(), emergencyCfg, emergencyPresent); err != nil {
		return fmt.Errorf("seed emergency config: %w", err)
	}
	logger.Info("emergency config seeded", "enabled", emergencyCfg.Enabled, "operation_timeout", emergencyCfg.OperationTimeout, "present", emergencyPresent)
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
	valuesConfig := s.cfg.Values.WithDefaults()
	svc := orchestrator.NewService(
		s.store,
		verifier,
		s.targetEnv,
		s.auditEmitter,
		emergencyDispatcher,
		orchestrator.NewProcessStreamRevoker(s.operatorRegistry()),
		s.operatorEndpoint(),
		createOperation,
		s.authorizer,
		orchestrator.ValuesConfig{
			MaxDocumentBytes: valuesConfig.MaxDocumentBytes,
			SecretPatterns:   valuesConfig.SecretPatterns,
		},
		logger,
	)
	s.emergency = svc
	if !s.cfg.Maintenance {
		// ADR-009 restart recovery: operations interrupted mid-preflight resume
		// coordination after the generic terminal-state recovery pass.
		resumed, resumeErr := svc.ResumePreflights(context.Background())
		if resumeErr != nil {
			logger.Warn("preflight resume failed", "err", resumeErr)
		} else if resumed > 0 {
			logger.Warn("preflight operations resumed on restart", "count", resumed)
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
	// The shared AuthInterceptor enforces against this Casbin enforcer. It
	// must hot-reload the durable policy like cmd/auth does: the dev
	// fixture's Initialize creates the organization + platform_admin AFTER
	// the orchestrator has booted, so a boot-time-only snapshot stays empty
	// and every post-seed protected call is denied (real smoke 2026-08-24:
	// devseed identity phase `list customers: permission_denied` with an
	// empty policy). The reloader keeps the interceptor fresh on the same
	// cadence as the auth service.
	s.enforcer = enforcer

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
	s.cleanup = orchestrator.NewCleanupService(s.store, retention, logger, s.auditEmitter)
	cleanupPath, cleanupHandler := orchestratorv1connect.NewCleanupServiceHandler(
		s.cleanup,
		connect.WithInterceptors(
			contractsinterceptor.NewRequestIDInterceptor(logger),
			contractsinterceptor.NewErrorSanitizeInterceptor(logger),
			app.MaintenanceInterceptor(s.cfg.Maintenance, nil, logger),
			auth.NewAuthInterceptor(jwtMgr, s.store, enforcer, map[string]bool{}, logger),
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
				// REQ-011 §562 bundle ingress (D-100 选项 B dev wiring):
				// the webhook forwards its dev service token; the actor is
				// service:release-webhook and the token is scoped to
				// SubmitReleaseBundle only (REQ-011 §561, v21 Step 6 风险行).
				auth.ServiceTokenInterceptor("release-webhook", s.serviceTokens(), logger,
					orchestratorv1connect.BundleServiceSubmitBundleProcedure),
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
	gc := orchestrator.DefaultGcConfig()
	retention := orchestrator.DefaultRetentionConfig()
	if s.configPath != "" {
		v := viper.New()
		v.SetConfigFile(s.configPath)
		v.SetConfigType("yaml")
		if err := v.ReadInConfig(); err == nil {
			if err := v.UnmarshalKey("gc", &gc); err != nil {
				return retention, fmt.Errorf("unmarshal gc config: %w", err)
			}
		}
	}
	for _, diagnostic := range gc.Diagnostics() {
		slog.Warn("gc configuration warning", "code", diagnostic.Code, "message", diagnostic.Message)
	}
	if err := gc.Validate(); err != nil {
		return retention, fmt.Errorf("validate gc config: %w", err)
	}
	// The cleanup engine retains its existing call surface while the input
	// layer uses the REQ-069 gc.* names. Prepare-session settings remain local
	// cleanup settings until their dedicated configuration migration.
	retention.BundleRetentionDays = gc.BundleRetentionDays
	retention.ArchiveGraceDays = gc.ArchiveGraceDays
	retention.CandidateArtifactRetentionDays = gc.CandidateArtifactRetentionDays
	retention.PreflightRetentionDays = gc.PreflightRetentionDays
	retention.OrphanPreflightRetentionDays = gc.OrphanPreflightRetentionDays
	retention.GCInterval = gc.Interval
	if gc.Interval > 0 {
		retention.GCIntervalHours = int(gc.Interval / time.Hour)
		if retention.GCIntervalHours < 1 {
			retention.GCIntervalHours = 1
		}
	} else {
		retention.GCIntervalHours = 0
	}
	retention.GCMaxDurationMinutes = gc.GCMaxDurationMinutes
	retention.CleanupIdempotencyRetentionHours = gc.CleanupIdempotencyRetentionHours
	if err := retention.Validate(); err != nil {
		return retention, fmt.Errorf("validate cleanup config: %w", err)
	}
	return retention, nil
}

type trustConfig struct {
	VerificationTimeout time.Duration `mapstructure:"verification_timeout"`
}

func defaultTrustConfig() trustConfig {
	return trustConfig{VerificationTimeout: trust.DefaultVerificationTimeout}
}

// loadEmergencyConfig reads the emergency.* keys from the service config
// (REQ-081 D2=A): a missing section fails closed to enabled=false and the
// D16 default operation timeout, reported via present=false so the startup
// seed leaves an already-configured app_settings value untouched; an
// unparsable/non-positive timeout falls back to the default while keeping
// the configured enabled flag.
func (s *orchSvc) loadEmergencyConfig() (config.EmergencyCfg, bool, error) {
	emergencyCfg := config.EmergencyCfg{Enabled: false, OperationTimeout: store.DefaultEmergencyOperationTimeout.String()}
	if s.configPath == "" {
		return emergencyCfg, false, nil
	}
	v := viper.New()
	v.SetConfigFile(s.configPath)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return emergencyCfg, false, fmt.Errorf("read emergency config: %w", err)
	}
	var raw struct {
		Enabled          bool   `mapstructure:"enabled"`
		OperationTimeout string `mapstructure:"operation_timeout"`
	}
	if err := v.UnmarshalKey("emergency", &raw); err != nil {
		return emergencyCfg, false, fmt.Errorf("unmarshal emergency config: %w", err)
	}
	present := v.IsSet("emergency")
	emergencyCfg.Enabled = raw.Enabled
	if raw.OperationTimeout != "" {
		if parsed, parseErr := time.ParseDuration(raw.OperationTimeout); parseErr == nil && parsed > 0 {
			emergencyCfg.OperationTimeout = parsed.String()
		}
	}
	return emergencyCfg, present, nil
}

// seedEmergencyConfig is the production writer of EmergencyConfigStore
// (REQ-081 D2=A): it upserts the startup emergency configuration into
// app_settings before the service accepts requests. When the config carries
// no emergency section (present=false) an existing app_settings value is
// preserved — a missing config fails closed only for a fresh database; an
// explicit enabled:false still overrides. The kill switch gate in
// ExecuteEmergencyChange keeps failing closed on a missing configuration
// (ADR-011).
func (s *orchSvc) seedEmergencyConfig(ctx context.Context, cfg config.EmergencyCfg, present bool) error {
	if !present {
		return nil
	}
	timeout := store.DefaultEmergencyOperationTimeout
	if parsed, err := time.ParseDuration(cfg.OperationTimeout); err == nil && parsed > 0 {
		timeout = parsed
	}
	return s.store.EmergencyConfig().SetEmergencyConfig(ctx, store.EmergencyConfig{
		Enabled:          cfg.Enabled,
		OperationTimeout: timeout,
	})
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
		orchestratorv1connect.OrchestratorServiceGetValuesRevisionProcedure:        {},
		orchestratorv1connect.OrchestratorServiceListValuesRevisionsProcedure:      {},
		orchestratorv1connect.OrchestratorServiceGetPrepareSessionProcedure:        {},
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
	// Durable authorization policy hot-reload (shared interceptor freshness;
	// see the enforcer wiring above). Maintenance mode skips background work.
	if s.enforcer != nil && !s.cfg.Maintenance {
		go s.enforcer.StartPolicyReloader(ctx, s.cfg.Authorization.WithDefaults().PolicyReloadInterval)
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

// serviceTokens returns the bundle ingress service token hashes
// (REQ-011 §562 dev minimal wiring, D-100 选项 B, AC-065-33): the current
// and previous tokens load from DEV_WEBHOOK_SERVICE_TOKEN /
// DEV_WEBHOOK_SERVICE_TOKEN_PREVIOUS and are stored as SHA-256 hex digests —
// the plaintext never leaves the env; ServiceTokenInterceptor compares the
// inbound bearer constant-time against these hashes. Empty env returns nil:
// production/non-dev behavior is unchanged (bundle ingress keeps requiring
// the JWT path, and no token is silently accepted).
func (s *orchSvc) serviceTokens() []string {
	current := strings.TrimSpace(os.Getenv("DEV_WEBHOOK_SERVICE_TOKEN"))
	previous := strings.TrimSpace(os.Getenv("DEV_WEBHOOK_SERVICE_TOKEN_PREVIOUS"))
	var tokens []string
	if current != "" {
		tokens = append(tokens, auth.SHA256Hash([]byte(current)))
	}
	if previous != "" {
		tokens = append(tokens, auth.SHA256Hash([]byte(previous)))
	}
	return tokens
}

func loadSourceRegistries(logger *slog.Logger) []orchestrator.SourceRegistry {
	_ = logger
	return nil
}

func main() {
	configPath := flag.String("config", "configs/orchestrator.dev.yaml", "path to config file")
	targetEnv := flag.String("target-env", "staging", "target environment (production, staging)")
	// REQ-065 D3: the dev lifecycle injects the JWT signing key through the
	// release-manager-jwt Secret as the JWT_SIGNING_KEY env var; the flag
	// default falls back to it so the Kustomize Deployment needs no static
	// key in args (Kubernetes cannot expand secretKeyRef values in args).
	signingKey := flag.String("signing-key", envOr("JWT_SIGNING_KEY", "change-me-in-production"), "JWT signing key")
	flag.Parse()

	app.Run(*configPath, &orchSvc{targetEnv: *targetEnv, configPath: *configPath, signingKey: *signingKey})
}

// envOr returns the environment value or the fallback when unset.
func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
