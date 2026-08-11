// Package main starts the release-auth service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	redis "github.com/redis/go-redis/v9"

	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/config"
	contractsinterceptor "github.com/ndzuki/release-manager/internal/contracts/interceptor"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	redisstore "github.com/ndzuki/release-manager/internal/store/redis"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/migrations"
)

type authSvc struct {
	cfg           config.ServiceConfig
	signingKey    string
	store         store.Store
	redisClient   *redis.Client
	pingDB        func(context.Context) error
	pingRedis     func(context.Context) error
	cancel        context.CancelFunc
	traceShutdown func(context.Context) error
}

func (s *authSvc) Name() string { return "release-auth" }

func (s *authSvc) Configure(cfg *config.ServiceConfig) { s.cfg = *cfg }

func (s *authSvc) Shutdown(ctx context.Context) error {
	if s.traceShutdown != nil {
		return s.traceShutdown(ctx)
	}
	return nil
}

func (s *authSvc) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	var errs []error
	if s.redisClient != nil {
		errs = append(errs, s.redisClient.Close())
	}
	if s.store != nil {
		errs = append(errs, s.store.Close())
	}
	return errors.Join(errs...)
}

func (s *authSvc) ReadinessChecks() map[string]func() error {
	checks := make(map[string]func() error, 2)
	if s.pingDB != nil {
		checks["database"] = func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return s.pingDB(ctx)
		}
	}
	if s.pingRedis != nil {
		checks["redis"] = func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return s.pingRedis(ctx)
		}
	}
	if len(checks) == 0 {
		return nil
	}
	return checks
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

	if s.cfg.Redis.Address != "" {
		s.redisClient = redis.NewClient(&redis.Options{
			Addr:                  s.cfg.Redis.Address,
			Password:              s.cfg.Redis.Password,
			DB:                    s.cfg.Redis.DB,
			MaxRetries:            -1,
			MinRetryBackoff:       -1,
			MaxRetryBackoff:       -1,
			DialerRetries:         1,
			DialerRetryTimeout:    10 * time.Millisecond,
			DialTimeout:           time.Second,
			ReadTimeout:           time.Second,
			WriteTimeout:          time.Second,
			ContextTimeoutEnabled: true,
		})
		if err := s.redisClient.Ping(context.Background()).Err(); err != nil {
			return fmt.Errorf("ping redis: %w", err)
		}
		s.pingRedis = func(ctx context.Context) error { return s.redisClient.Ping(ctx).Err() }
		s.store = &sessionStore{Store: s.store, authSessions: redisstore.New(s.redisClient, s.store.AuthSessions())}
		logger.Info("redis session adapter enabled", "address", s.cfg.Redis.Address, "db", s.cfg.Redis.DB)
	}

	metrics := authorization.NewMetrics(prometheus.NewRegistry())
	mux.Handle("GET /metrics", metrics.Handler())
	s.traceShutdown = authorization.InstallTracing()
	authzConfig := s.cfg.Authorization.WithDefaults()
	jwtMgr := auth.NewJWTManager([]byte(s.signingKey), 15*time.Minute, 7*24*time.Hour)
	limiter := auth.NewRateLimiter(5, time.Minute)
	resolver := auth.StubResolver{}

	enforcer, err := auth.NewEnforcer(s.store, logger, metrics)
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
		enforcer.StartPolicyReloader(ctx, authzConfig.PolicyReloadInterval)
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
		contractsinterceptor.NewRequestIDInterceptor(logger),
		contractsinterceptor.NewErrorSanitizeInterceptor(logger),
		authorization.TraceInterceptor(),
		app.MaintenanceInterceptor(s.cfg.Maintenance, readOnly, logger),
		auth.NewAuthInterceptor(jwtMgr, s.store, enforcer, publicMethods, logger),
	)

	authService := auth.NewAuthService(s.store, jwtMgr, limiter, logger, enforcer)
	authPath, authHandler := authv1connect.NewAuthServiceHandler(authService, interceptorOpt)
	mux.Handle(authPath, authHandler)

	orgSvc := auth.NewOrgService(s.store, logger, enforcer)
	orgPath, orgHandler := authv1connect.NewOrganizationServiceHandler(orgSvc, interceptorOpt)
	mux.Handle(orgPath, orgHandler)

	bindingSvc := auth.NewBindingService(s.store, resolver, logger)
	bindingPath, bindingHandler := authv1connect.NewBindingServiceHandler(bindingSvc, interceptorOpt)
	mux.Handle(bindingPath, bindingHandler)

	authorizationSvc := auth.NewAuthorizationService(s.store, enforcer, metrics, logger)
	authorizationPath, authorizationHandler := authv1connect.NewAuthorizationServiceHandler(authorizationSvc, interceptorOpt)
	mux.Handle(authorizationPath, authorizationHandler)

	return nil
}

type sessionStore struct {
	store.Store
	authSessions store.AuthSessionStore
}

func (s *sessionStore) AuthSessions() store.AuthSessionStore { return s.authSessions }

func authReadOnlyProcedures() map[string]struct{} {
	return map[string]struct{}{
		authv1connect.AuthServiceGetInitStatusProcedure:                     {},
		authv1connect.AuthServiceValidateTokenProcedure:                     {},
		authv1connect.AuthServiceGetLocalUserProcedure:                      {},
		authv1connect.AuthServiceListLocalUsersProcedure:                    {},
		authv1connect.OrganizationServiceGetOrganizationProcedure:           {},
		authv1connect.OrganizationServiceListOrganizationsProcedure:         {},
		authv1connect.OrganizationServiceListMembersProcedure:               {},
		authv1connect.BindingServiceGetBindingProcedure:                     {},
		authv1connect.BindingServiceListBindingsProcedure:                   {},
		authv1connect.AuthorizationServiceGetAuthorizationSnapshotProcedure: {},
		authv1connect.ExternalIdentityServiceGetOIDCAuthURLProcedure:        {},
		authv1connect.ExternalIdentityServiceGetDingTalkAuthURLProcedure:    {},
	}
}
func main() {
	configPath := flag.String("config", "configs/auth.dev.yaml", "path to config file")
	signingKey := flag.String("signing-key", "change-me-in-production", "JWT signing key")
	flag.Parse()

	app.Run(*configPath, &authSvc{signingKey: *signingKey})
}
