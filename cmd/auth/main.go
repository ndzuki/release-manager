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
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type authSvc struct {
	dbPath        string
	signingKey    string
	secureCookies bool
}

func (s *authSvc) Name() string { return "release-auth" }

func (s *authSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	signingKey := []byte(s.signingKey)

	jwtMgr := auth.NewJWTManager(signingKey, 15*time.Minute, 7*24*time.Hour)
	limiter := auth.NewRateLimiter(5, time.Minute)
	resolver := auth.StubResolver{}

	enforcer, err := auth.NewEnforcer(st, logger)
	if err != nil {
		return err
	}
	//nolint:staticcheck // context.TODO is appropriate for startup initialization
	if err := enforcer.LoadPolicies(context.TODO()); err != nil {
		logger.Error("initial policy load failed", "error", err)
	}
	enforcer.StartPolicyReloader(context.TODO(), 30*time.Second)

	publicMethods := map[string]bool{
		authv1connect.AuthServiceGetInitStatusProcedure:      true,
		authv1connect.AuthServiceInitializeProcedure:         true,
		authv1connect.AuthServiceLoginProcedure:              true,
		authv1connect.AuthServiceRefreshTokenProcedure:       true,
		authv1connect.AuthServiceValidateTokenProcedure:      true,
		authv1connect.AuthServiceLogoutProcedure:             true,
		authv1connect.AuthServiceSwitchOrganizationProcedure: true,
		authv1connect.AuthServiceChangePasswordProcedure:     true,
	}

	interceptor := auth.NewAuthInterceptor(jwtMgr, enforcer, publicMethods, logger)
	interceptorOpt := connect.WithInterceptors(interceptor)

	authSvc := auth.NewAuthService(st, jwtMgr, limiter, logger, auth.BrowserSessionConfig{
		SecureCookies: s.secureCookies,
	})
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
	secureCookies := flag.Bool("secure-cookies", true, "require HTTPS for browser session cookies")
	flag.Parse()

	app.Run(*configPath, &authSvc{
		dbPath:        *dbPath,
		signingKey:    *signingKey,
		secureCookies: *secureCookies,
	})
}
