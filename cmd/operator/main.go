// Package main starts the release-operator service.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"time"

	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type operatorSvc struct {
	dbPath string
	st     *sqlitestore.Store
}

func (s *operatorSvc) Name() string { return "release-operator" }

func (s *operatorSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	s.st = st
	logger.Info("store opened", "db", s.dbPath)

	svc := operator.NewService(st, logger)
	path, h := operatorv1connect.NewOperatorServiceHandler(svc)
	mux.Handle(path, h)

	// Start session expiration goroutine.
	go s.runSessionExpiry(context.Background(), logger)

	return nil
}

func (s *operatorSvc) runSessionExpiry(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.st == nil {
				continue
			}
			expired, err := s.st.Sessions().ListExpiredSuspect(ctx, 60*time.Second)
			if err != nil {
				logger.Warn("failed to list expired sessions", "error", err)
				continue
			}
			for _, sess := range expired {
				nextStatus := store.SessionStatus("offline")
				if sess.Status == store.SessionOnline {
					nextStatus = store.SessionSuspect
				}
				logger.Debug("session expiry transition",
					"session_id", sess.ID,
					"from", sess.Status,
					"to", nextStatus,
				)
				if err := s.st.Sessions().UpdateStatus(ctx, sess.ID, nextStatus); err != nil {
					logger.Warn("failed to update expired session status", "error", err)
				}
			}
		}
	}
}

func main() {
	configPath := flag.String("config", "configs/operator.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/operator.db", "path to SQLite database")
	flag.Parse()

	app.Run(*configPath, &operatorSvc{dbPath: *dbPath})
}
