// Package main starts the release-api service.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"

	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/handler"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type apiSvc struct {
	dbPath     string
	signingKey string
	store      *sqlitestore.Store
	emitter    *audit.Emitter
	closeOnce  sync.Once
	closeErr   error
}

func (s *apiSvc) Name() string { return "release-api" }

func (s *apiSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	valsHandler := handler.NewValuesHandler(st.Values(), 0, logger)
	valsHandler.Register(mux)

	jwtMgr := auth.NewJWTManager([]byte(s.signingKey), 15*time.Minute, 7*24*time.Hour)
	s.store = st
	s.emitter = audit.NewEmitter(st.AuditEvents(), logger, audit.DefaultConfig())
	auditSvc := audit.NewService(s.emitter)
	auditPath, auditHandler := auditv1connect.NewAuditServiceHandler(
		auditSvc,
		connect.WithInterceptors(audit.NewJWTInterceptor(jwtMgr)),
	)
	mux.Handle(auditPath, auditHandler)

	return nil
}

func (s *apiSvc) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
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

func main() {
	configPath := flag.String("config", "configs/api.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/api.db", "path to SQLite database")
	signingKey := flag.String("signing-key", "change-me-in-production", "JWT signing key")
	flag.Parse()

	app.Run(*configPath, &apiSvc{dbPath: *dbPath, signingKey: *signingKey})
}
