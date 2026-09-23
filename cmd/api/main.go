// Package main starts the release-api service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"

	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/config"
	contractsinterceptor "github.com/ndzuki/release-manager/internal/contracts/interceptor"

	"github.com/ndzuki/release-manager/internal/jwtauth"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type apiSvc struct {
	dbPath        string
	jwtPublicKey  string
	configPath    string
	store         *sqlitestore.Store
	emitter       *audit.Emitter
	archiveWorker *audit.ArchiveWorker
	closeOnce     sync.Once
	closeErr      error

	// decisionClient overrides the release-auth authorization client. Tests inject
	// a stub; production builds it from authorization.auth_url (ADR-021).
	decisionClient audit.DecisionClient

	// auditFlushInterval overrides the audit emitter flush interval. Tests set a
	// short interval so an emitted event becomes queryable without waiting for the
	// production batch tick (audit.DefaultConfig uses 5s); zero keeps the default.
	auditFlushInterval time.Duration
}

func (s *apiSvc) Name() string { return "release-api" }

// Compile-time proof that app.Run's optional lifecycle interfaces are actually
// satisfied (TASK-094 §7-9: RunBackground/Close silently matched nothing).
var (
	_ interface{ Run(context.Context) } = (*apiSvc)(nil)
	_ interface{ Close() error }        = (*apiSvc)(nil)
)

func (s *apiSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	jwtPublicKey, err := jwtauth.ParseEd25519PublicKeyPEM(s.jwtPublicKey)
	if err != nil {
		return fmt.Errorf("jwt verification key: %w", err)
	}
	jwtMgr := jwtauth.New(jwtPublicKey, 15*time.Minute)
	s.store = st
	svcCfg, loadErr := config.LoadService(s.configPath)
	if loadErr != nil {
		logger.Warn("cannot load service config, using defaults", "error", loadErr)
	}
	emitterCfg := audit.DefaultConfig()
	if s.auditFlushInterval > 0 {
		emitterCfg.FlushInterval = s.auditFlushInterval
	}
	s.emitter = audit.NewEmitter(st.AuditEvents(), logger, emitterCfg)
	decisions := s.decisionClient
	if decisions == nil {
		authzCfg := config.AuthorizationCfg{}
		if svcCfg != nil {
			authzCfg = svcCfg.Authorization
		}
		authzCfg = authzCfg.WithDefaults()
		decisions = audit.NewConnectDecisionClient(authv1connect.NewAuthorizationServiceClient(
			http.DefaultClient,
			authzCfg.AuthURL,
		))
		logger.Info("audit authorization decisions wired", "auth_url", authzCfg.AuthURL)
	}
	auditSvc := audit.NewAuditServiceHandler(st, s.emitter, logger, decisions)
	auditPath, auditHandler := auditv1connect.NewAuditServiceHandler(
		auditSvc,
		connect.WithInterceptors(
			contractsinterceptor.NewRequestIDInterceptor(logger),
			contractsinterceptor.NewErrorSanitizeInterceptor(logger),
			audit.NewJWTInterceptor(jwtMgr),
		),
	)
	mux.Handle(auditPath, auditHandler)

	// Wire archive worker.
	archCfg := archiveConfigFromService(svcCfg)
	sink := audit.NewFileSystemSink()
	archiver := audit.NewArchiver(st.AuditEvents(), sink)
	s.archiveWorker = audit.NewArchiveWorker(archiver, archCfg, logger)

	return nil
}

// Run implements app.Run's optional backgroundService interface
// (Run(context.Context)) and starts the audit archive worker loop. The
// previous RunBackground(ctx, *slog.Logger) signature never matched the
// interface, so the worker never started and the audit.archive.* config keys
// had no runtime effect (TASK-094 §7-9).
func (s *apiSvc) Run(ctx context.Context) {
	if s.archiveWorker != nil {
		s.archiveWorker.Run(ctx)
	}
}

// Close implements app.Run's optional closeService interface (Close() error)
// so shutdown flushes the audit emitter and closes the store. The old
// Close(ctx) error signature did not match either, which silently dropped the
// last events on exit. The flush budget mirrors app.Run's 5s shutdown window.
func (s *apiSvc) Close() error {
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var errs []error
		if s.emitter != nil {
			if err := s.emitter.Shutdown(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		if s.store != nil {
			if err := s.store.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

func archiveConfigFromService(cfg *config.ServiceConfig) audit.ArchiveConfig {
	if cfg == nil {
		return audit.DefaultArchiveConfig()
	}
	return audit.ArchiveConfig{
		RetentionDays:     cfg.Audit.Archive.RetentionDays,
		PollInterval:      cfg.Audit.Archive.PollInterval,
		BatchSize:         cfg.Audit.Archive.BatchSize,
		ArchiveDir:        cfg.Audit.Archive.ArchiveDir,
		Compression:       cfg.Audit.Archive.Compression,
		ChecksumAlgorithm: cfg.Audit.Archive.ChecksumAlgorithm,
	}
}

// envOr returns the environment value or the fallback when unset.
func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	configPath := flag.String("config", "configs/api.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/api.db", "path to SQLite database")
	// REQ-065 AC-065-01 / D1=A: the audit API only verifies management-plane
	// access tokens, so it holds the Ed25519 public key and never the signing
	// key. See internal/jwtauth.
	publicKeyPEM := flag.String("jwt-public-key", envOr("JWT_PUBLIC_KEY", ""), "PEM-encoded Ed25519 JWT verification key")
	flag.Parse()

	app.Run(*configPath, &apiSvc{
		dbPath:       *dbPath,
		jwtPublicKey: *publicKeyPEM,
		configPath:   *configPath,
	})
}
