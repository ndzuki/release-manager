// Package main starts the release-operator service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/audit"
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
	kubeConfig      string
	sessionID       string
	operatorID      string
	customerID      string
	clusterID       string
	installAtomic   bool
	installTimeout  time.Duration
	st              *sqlitestore.Store
	syncer          *operator.InventorySyncer
	agent           *operatoragent.Agent
	commandStore    localstore.Store
	auditEmitter    *audit.Emitter
}

func (s *operatorSvc) Name() string { return "release-operator" }

func (s *operatorSvc) ConfigureServer(server *http.Server) error {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server.Protocols = protocols
	return nil
}

func (s *operatorSvc) Run(ctx context.Context) {
	if s.syncer != nil {
		s.syncer.Start(ctx)
	}
	go s.runSessionExpiry(ctx, slog.Default())
	if s.agent != nil {
		if err := s.agent.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("operator agent stopped", "error", err)
		}
	}
}

func (s *operatorSvc) Shutdown(ctx context.Context) error {
	if s.auditEmitter == nil {
		return nil
	}
	return s.auditEmitter.Shutdown(ctx)
}

func (s *operatorSvc) Close() error {
	var closeErr error
	if s.commandStore != nil {
		closeErr = errors.Join(closeErr, s.commandStore.Close())
	}
	if s.st != nil {
		closeErr = errors.Join(closeErr, s.st.Close())
	}
	return closeErr
}

func (s *operatorSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	s.st = st
	logger.Info("store opened", "db", s.dbPath)

	s.auditEmitter = audit.NewEmitter(st.AuditEvents(), logger, audit.DefaultConfig())
	svc, err := operator.NewService(st, logger, s.auditEmitter)
	if err != nil {
		return fmt.Errorf("create operator service: %w", err)
	}
	path, handler := operatorv1connect.NewOperatorServiceHandler(svc)
	mux.Handle(path, handler)

	engine := helmengine.NewRealEngine(s.kubeConfig, logger)
	secretClient, err := newSecretClient(s.kubeConfig)
	if err != nil {
		return fmt.Errorf("create Kubernetes secret client: %w", err)
	}
	if s.orchestratorURL != "" {
		orchClient := orchestratorv1connect.NewOrchestratorServiceClient(
			http.DefaultClient,
			s.orchestratorURL,
		)
		s.syncer = operator.NewInventorySyncer(
			engine,
			orchClient,
			s.operatorID,
			s.customerID,
			s.clusterID,
			logger,
		)

		if s.sessionID != "" && s.operatorID != "" {
			commandStore, err := localstore.OpenBolt(s.commandDBPath)
			if err != nil {
				return fmt.Errorf("open command store: %w", err)
			}
			s.commandStore = commandStore
			kubernetesClient, err := operator.NewKubernetesClient(s.kubeConfig)
			if err != nil {
				return fmt.Errorf("create emergency Kubernetes client: %w", err)
			}
			operatorClient := operatorv1connect.NewOperatorServiceClient(
				http.DefaultClient,
				s.orchestratorURL,
			)
		s.agent, err = operatoragent.New(operatoragent.Config{
				Client:            operatoragent.ConnectClient{Client: operatorClient},
				Engine:            engine,
				Store:             commandStore,
				Notifier:          s.syncer,
				SyncExecutor:      s.syncer,
				Secrets:           secretClient.CoreV1(),
				EmergencyExecutor: operator.NewEmergencyCommandExecutor(kubernetesClient),
				SessionID:         s.sessionID,
				OperatorID:        s.operatorID,
				Logger:            logger,
				InstallFlags: operatoragent.InstallFlags{
					Atomic:  s.installAtomic,
					Timeout: s.installTimeout,
				},
			})
			if err != nil {
				return fmt.Errorf("create operator agent: %w", err)
			}
		}

		logger.Info("operator runtime wired", "orchestrator_url", s.orchestratorURL)
	}

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

func newSecretClient(kubeConfig string) (kubernetes.Interface, error) {
	var (
		config *rest.Config
		err    error
	)
	if kubeConfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("load Kubernetes REST config: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("initialize Kubernetes clientset: %w", err)
	}
	return client, nil
}

func main() {
	configPath := flag.String("config", "configs/operator.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/operator.db", "path to SQLite database")
	commandDBPath := flag.String("command-db", "data/operator-commands.db", "path to durable command database")
	orchestratorAddr := flag.String("orchestrator-addr", "http://localhost:8083", "orchestrator Connect URL")
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
		kubeConfig:      *kubeConfig,
		sessionID:       *sessionID,
		operatorID:      *operatorID,
		customerID:      *customerID,
		clusterID:       *clusterID,
		installAtomic:   *installAtomic,
		installTimeout:  *installTimeout,
	})
}
