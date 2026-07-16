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
	"github.com/ndzuki/release-manager/internal/operator/agent"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"k8s.io/client-go/rest"
)

type operatorSvc struct {
	dbPath           string
	orchestratorURL  string
	operatorURL      string
	helmEngineMode   string
	commandStorePath string
	sessionID        string
	operatorID       string
	st               *sqlitestore.Store
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
	path, handler := operatorv1connect.NewOperatorServiceHandler(svc)
	mux.Handle(path, handler)

	engine := helmengine.Engine(helmengine.NewFake())
	if s.helmEngineMode == "real" {
		engine = helmengine.NewRealEngine("", func() *rest.Config {
			config, configErr := rest.InClusterConfig()
			if configErr != nil {
				logger.Warn("failed to load in-cluster kubernetes config", "error", configErr)
				return nil
			}
			return config
		})
	}

	var onComplete func(namespace, releaseName, operationID string)
	if s.orchestratorURL != "" {
		orchClient := orchestratorv1connect.NewOrchestratorServiceClient(
			http.DefaultClient,
			s.orchestratorURL,
		)
		syncer := operator.NewInventorySyncer(engine, orchClient, "", "", "", logger)
		svc.SetInventorySyncer(syncer)
		onComplete = syncer.NotifyOperationComplete
		go syncer.Start(context.Background())
		logger.Info("inventory syncer wired",
			"orchestrator_url", s.orchestratorURL,
			"helm_engine", s.helmEngineMode,
		)
	}

	if s.sessionID != "" && s.operatorID != "" {
		commandStore, storeErr := localstore.OpenBolt(s.commandStorePath)
		if storeErr != nil {
			return fmt.Errorf("open command store: %w", storeErr)
		}
		cmdAgent, agentErr := agent.New(agent.Options{
			Client:     operatorv1connect.NewOperatorServiceClient(http.DefaultClient, s.operatorURL),
			Engine:     engine,
			Store:      commandStore,
			SessionID:  s.sessionID,
			OperatorID: s.operatorID,
			Logger:     logger,
			OnComplete: onComplete,
		})
		if agentErr != nil {
			return fmt.Errorf("create command agent: %w", agentErr)
		}
		go func() {
			if runErr := cmdAgent.Run(context.Background()); runErr != nil {
				logger.Error("command agent stopped", "error", runErr)
			}
		}()
		logger.Info("command agent wired", "operator_id", s.operatorID)
	}

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
	operatorAddr := flag.String("operator-addr", "http://localhost:8084", "operator Connect URL")
	helmEngineMode := flag.String("helm-engine", "fake", "Helm engine mode: fake or real")
	commandStorePath := flag.String("command-store", "data/operator-commands.db", "path to the local command store")
	sessionID := flag.String("session-id", "", "enrolled operator session ID")
	operatorID := flag.String("operator-id", "", "enrolled operator ID")
	flag.Parse()

	app.Run(*configPath, &operatorSvc{
		dbPath:           *dbPath,
		orchestratorURL:  *orchestratorAddr,
		operatorURL:      *operatorAddr,
		helmEngineMode:   *helmEngineMode,
		commandStorePath: *commandStorePath,
		sessionID:        *sessionID,
		operatorID:       *operatorID,
	})
}
