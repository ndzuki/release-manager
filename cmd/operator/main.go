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
	operatoragent "github.com/ndzuki/release-manager/internal/operator/agent"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type operatorSvc struct {
	dbPath          string
	commandDBPath   string
	orchestratorURL string
	operatorURL     string
	kubeConfig      string
	sessionID       string
	operatorID      string
	customerID      string
	clusterID       string
	installAtomic   bool
	installTimeout  time.Duration
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

	engine := helmengine.NewRealEngine(s.kubeConfig, logger)
	if s.orchestratorURL != "" {
		orchClient := orchestratorv1connect.NewOrchestratorServiceClient(
			http.DefaultClient,
			s.orchestratorURL,
		)
		syncer := operator.NewInventorySyncer(
			engine,
			orchClient,
			s.operatorID,
			s.customerID,
			s.clusterID,
			logger,
		)
		svc.SetInventorySyncer(syncer)
		syncer.Start(context.Background())

		if s.sessionID != "" && s.operatorID != "" {
			commandStore, err := localstore.OpenBolt(s.commandDBPath)
			if err != nil {
				return fmt.Errorf("open command store: %w", err)
			}
			operatorClient := operatorv1connect.NewOperatorServiceClient(
				http.DefaultClient,
				s.operatorURL,
			)
			agent, err := operatoragent.New(operatoragent.Config{
				Client:     operatoragent.ConnectClient{Client: operatorClient},
				Engine:     engine,
				Store:      commandStore,
				Notifier:   syncer,
				SessionID:  s.sessionID,
				OperatorID: s.operatorID,
				Logger:     logger,
				InstallFlags: operatoragent.InstallFlags{
					Atomic:  s.installAtomic,
					Timeout: s.installTimeout,
				},
			})
			if err != nil {
				return fmt.Errorf("create operator agent: %w", err)
			}
			go func() {
				if err := agent.Run(context.Background()); err != nil {
					logger.Error("operator agent stopped", "error", err)
				}
			}()
		}

		logger.Info("operator runtime wired", "orchestrator_url", s.orchestratorURL)
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
	commandDBPath := flag.String("command-db", "data/operator-commands.db", "path to durable command database")
	orchestratorAddr := flag.String("orchestrator-addr", "http://localhost:8083", "orchestrator Connect URL")
	operatorAddr := flag.String("operator-addr", "http://localhost:8084", "operator Connect URL")
	kubeConfig := flag.String("kubeconfig", "", "path to kubeconfig; empty uses in-cluster or default config")
	sessionID := flag.String("session-id", "", "enrolled operator session ID")
	operatorID := flag.String("operator-id", "", "enrolled operator ID")
	customerID := flag.String("customer-id", "", "operator customer ID")
	clusterID := flag.String("cluster-id", "", "operator cluster ID")
	installAtomic := flag.Bool("install-atomic", true, "uninstall failed releases atomically")
	installTimeout := flag.Duration("install-timeout", 5*time.Minute, "default Helm install timeout")
	flag.Parse()

	app.Run(*configPath, &operatorSvc{
		dbPath:          *dbPath,
		commandDBPath:   *commandDBPath,
		orchestratorURL: *orchestratorAddr,
		operatorURL:     *operatorAddr,
		kubeConfig:      *kubeConfig,
		sessionID:       *sessionID,
		operatorID:      *operatorID,
		customerID:      *customerID,
		clusterID:       *clusterID,
		installAtomic:   *installAtomic,
		installTimeout:  *installTimeout,
	})
}
