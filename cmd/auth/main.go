// Package main starts the release-auth service.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"

	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/auth"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type authSvc struct {
	ctx        context.Context
	dbPath     string
	signingKey string
	closeStore func() error
}

func (s *authSvc) Name() string { return "release-auth" }

func (s *authSvc) Close() error {
	if s.closeStore == nil {
		return nil
	}
	return s.closeStore()
}

func (s *authSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)
	s.closeStore = st.Close

	signingKey := []byte(s.signingKey)

	jwtMgr := auth.NewJWTManager(signingKey, 15*time.Minute, 7*24*time.Hour)
	limiter := auth.NewRateLimiter(5, time.Minute)
	resolver := auth.StubResolver{}
	auth.StartSessionCleanup(s.ctx, st.AuthSessions(), time.Hour, logger)

	enforcer, err := auth.NewEnforcer(st, logger)
	if err != nil {
		return err
	}
	//nolint:staticcheck // context.TODO is appropriate for startup initialization
	if err := enforcer.LoadPolicies(context.TODO()); err != nil {
		logger.Error("initial policy load failed", "error", err)
	}
	enforcer.StartPolicyReloader(s.ctx, 30*time.Second)

	publicMethods := map[string]bool{
		authv1connect.AuthServiceLoginProcedure:        true,
		authv1connect.AuthServiceRefreshTokenProcedure: true,
	}

	interceptor := auth.NewAuthInterceptor(jwtMgr, st, enforcer, publicMethods, logger)
	interceptorOpt := connect.WithInterceptors(interceptor)

	authSvc := auth.NewAuthService(st, jwtMgr, limiter, logger)
	authPath, authHandler := authv1connect.NewAuthServiceHandler(authSvc, interceptorOpt)
	mux.Handle(authPath, authHandler)

	orgSvc := auth.NewOrgService(st, logger)
	orgPath, orgHandler := authv1connect.NewOrganizationServiceHandler(orgSvc, interceptorOpt)
	mux.Handle(orgPath, orgHandler)

	bindingSvc := auth.NewBindingService(st, resolver, logger)
	bindingPath, bindingHandler := authv1connect.NewBindingServiceHandler(bindingSvc, interceptorOpt)
	mux.Handle(bindingPath, bindingHandler)

	return nil
}

func main() {
	configPath := flag.String("config", "configs/auth.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/auth.db", "path to SQLite database")
	signingKey := flag.String("signing-key", "change-me-in-production", "JWT signing key")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	svc := &authSvc{ctx: ctx, dbPath: *dbPath, signingKey: *signingKey}
	defer func() {
		if err := svc.Close(); err != nil {
			slog.Error("close auth store", "error", err)
		}
	}()
	app.Run(*configPath, svc)
}
