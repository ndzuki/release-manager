// Package main starts the release-operator service.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type operatorSvc struct {
	dbPath          string
	orchestratorURL string
	st              *sqlitestore.Store
}

func (s *operatorSvc) Name() string { return "release-operator" }

func (s *operatorSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	s.st = st
	logger.Info("store opened", "db", s.dbPath)

	svc, err := operator.NewService(st, logger)
	if err != nil {
		return fmt.Errorf("create operator service: %w", err)
	}
	path, h := operatorv1connect.NewOperatorServiceHandler(svc)
	mux.Handle(path, h)

	// Wire inventory syncer (REQ-017)
	if s.orchestratorURL != "" {
		orchClient := orchestratorv1connect.NewOrchestratorServiceClient(
			http.DefaultClient,
			s.orchestratorURL,
		)
		engine := helmengine.NewFake()
		syncer := operator.NewInventorySyncer(
			engine, orchClient,
			"", "", "", // operator_id, customer_id, cluster_id set on enrollment
			logger,
		)
		svc.SetInventorySyncer(syncer)
		// Start syncer in background; it uses placeholder IDs until enrollment populates them.
		go syncer.Start(context.Background())
		logger.Info("inventory syncer wired", "orchestrator_url", s.orchestratorURL)
	}

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
	orchestratorAddr := flag.String("orchestrator-addr", "http://localhost:8081", "orchestrator Connect URL")
	flag.Parse()

	app.Run(*configPath, &operatorSvc{dbPath: *dbPath, orchestratorURL: *orchestratorAddr})
}
