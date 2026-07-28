// Package main starts the release-auth service.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/migrations"
)

type authSvc struct {
	cfg        config.ServiceConfig
	signingKey string
	store      store.Store
	pingDB     func(context.Context) error
	cancel     context.CancelFunc
}

func (s *authSvc) Name() string { return "release-auth" }

func (s *authSvc) Configure(cfg *config.ServiceConfig) { s.cfg = *cfg }

func (s *authSvc) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.store == nil {
		return nil
	}
	return s.store.Close()
}

func (s *authSvc) ReadinessChecks() map[string]func() error {
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

func (s *authSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
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
	logger.Info("store opened", "driver", s.cfg.Database.Driver)

	// Create database ping closure for /readyz without modifying store.Store interface.
	switch store := s.store.(type) {
	case *postgresstore.Store:
		s.pingDB = store.SQLDB().PingContext
	case *sqlitestore.Store:
		s.pingDB = store.DB().PingContext
	}

	jwtMgr := auth.NewJWTManager([]byte(s.signingKey), 15*time.Minute, 7*24*time.Hour)
	limiter := auth.NewRateLimiter(5, time.Minute)
	resolver := auth.StubResolver{}

	enforcer, err := auth.NewEnforcer(s.store, logger)
	if err != nil {
		return err
	}
	if err := enforcer.LoadPolicies(context.Background()); err != nil {
		logger.Error("initial policy load failed", "error", err)
	}
	if !s.cfg.Maintenance {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		auth.StartSessionCleanup(ctx, s.store.AuthSessions(), time.Hour, logger)
		enforcer.StartPolicyReloader(ctx, 30*time.Second)
	}

	// Login and refresh create/revoke auth session state, so maintenance mode
	// deliberately rejects them even though they are public procedures.
	publicMethods := map[string]bool{
		authv1connect.AuthServiceGetInitStatusProcedure: true,
		authv1connect.AuthServiceInitializeProcedure:    true,
		authv1connect.AuthServiceValidateTokenProcedure: true,
		authv1connect.AuthServiceLoginProcedure:         true,
		authv1connect.AuthServiceRefreshTokenProcedure:  true,
	}
	readOnly := authReadOnlyProcedures()
	interceptorOpt := connect.WithInterceptors(
		app.MaintenanceInterceptor(s.cfg.Maintenance, readOnly, logger),
		auth.NewAuthInterceptor(jwtMgr, s.store, enforcer, publicMethods, logger),
	)

	authService := auth.NewAuthService(s.store, jwtMgr, limiter, logger)
	authPath, authHandler := authv1connect.NewAuthServiceHandler(authService, interceptorOpt)
	mux.Handle(authPath, authHandler)

	orgSvc := auth.NewOrgService(s.store, logger, enforcer)
	orgPath, orgHandler := authv1connect.NewOrganizationServiceHandler(orgSvc, interceptorOpt)
	mux.Handle(orgPath, orgHandler)

	bindingSvc := auth.NewBindingService(s.store, resolver, logger)
	bindingPath, bindingHandler := authv1connect.NewBindingServiceHandler(bindingSvc, interceptorOpt)
	mux.Handle(bindingPath, bindingHandler)

	return nil
}

func authReadOnlyProcedures() map[string]struct{} {
	return map[string]struct{}{
		authv1connect.AuthServiceGetInitStatusProcedure:                  {},
		authv1connect.AuthServiceValidateTokenProcedure:                  {},
		authv1connect.OrganizationServiceGetOrganizationProcedure:        {},
		authv1connect.OrganizationServiceListOrganizationsProcedure:      {},
		authv1connect.OrganizationServiceListMembersProcedure:            {},
		authv1connect.BindingServiceGetBindingProcedure:                  {},
		authv1connect.BindingServiceListBindingsProcedure:                {},
		authv1connect.ExternalIdentityServiceGetOIDCAuthURLProcedure:     {},
		authv1connect.ExternalIdentityServiceGetDingTalkAuthURLProcedure: {},
	}
}
func main() {
	configPath := flag.String("config", "configs/auth.dev.yaml", "path to config file")
	signingKey := flag.String("signing-key", "change-me-in-production", "JWT signing key")
	flag.Parse()

	app.Run(*configPath, &authSvc{signingKey: *signingKey})
}
