// Package main starts the release-operator service.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/config"
	contractsinterceptor "github.com/ndzuki/release-manager/internal/contracts/interceptor"
	"github.com/ndzuki/release-manager/internal/operator"
	operatoragent "github.com/ndzuki/release-manager/internal/operator/agent"
	"github.com/ndzuki/release-manager/internal/operator/bootstrap"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
	"github.com/ndzuki/release-manager/internal/operator/secretmetadata"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// gatewayMode keeps the management-plane operator deployment (authoritative
// store + OperatorService handler); TASK-065 removes this path. Any other
// mode value runs the default agent runtime.
const gatewayMode = "gateway"

type operatorSvc struct {
	dbPath              string
	commandDBPath       string
	orchestratorURL     string
	kubeConfig          string
	mode                string
	customerID          string
	clusterID           string
	operatorName        string
	caCertPath          string
	enrollmentTokenFile string
	installAtomic       bool
	installTimeout      time.Duration
	st                  *sqlitestore.Store
	syncer              *operator.InventorySyncer
	agent               *operatoragent.Agent
	commandStore        localstore.Store
	auditEmitter        *audit.Emitter
}

func (s *operatorSvc) Name() string { return "release-operator" }

func (s *operatorSvc) Configure(cfg *config.ServiceConfig) {
	agentCfg := cfg.Agent.WithDefaults()
	s.mode = agentCfg.Mode
	s.customerID = agentCfg.CustomerID
	s.clusterID = agentCfg.ClusterID
	s.operatorName = agentCfg.OperatorName
	s.enrollmentTokenFile = agentCfg.EnrollmentTokenFile
	s.caCertPath = cfg.CA.CertPath
}

//nolint:unparam // error return is mandated by the internal/app serverConfigurer interface
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
		runAgentLoop(ctx, s.agent, slog.Default())
	}
}

// runAgentLoop keeps the agent connected with exponential backoff (REQ-044:
// the agent keeps retrying after the session service rolls back). Agent.Run
// processes one stream lifetime; the loop reconnects with a fresh Hello.
func runAgentLoop(ctx context.Context, agent *operatoragent.Agent, logger *slog.Logger) {
	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second
	for {
		err := agent.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		logger.Warn("operator agent disconnected; reconnecting",
			"error", err, "retry_after", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, maxBackoff)
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
	if s.mode == gatewayMode {
		return s.registerGateway(mux, logger)
	}
	return s.registerAgent(logger)
}

// registerAgent wires the default agent runtime (TASK-075 plan v1 Step 7):
// no authoritative store, no OperatorService handler. The bbolt database
// holds commands and the bootstrap identity; the agent dials the gateway
// over mTLS with the enrolled certificate.
func (s *operatorSvc) registerAgent(logger *slog.Logger) error {
	commandStore, err := localstore.OpenBolt(s.commandDBPath)
	if err != nil {
		return fmt.Errorf("open command store: %w", err)
	}
	s.commandStore = commandStore

	if s.customerID == "" || s.clusterID == "" {
		return fmt.Errorf("agent mode requires agent.customer_id and agent.cluster_id")
	}
	ctx := context.Background()
	result, err := bootstrap.Bootstrap(ctx, bootstrap.Config{
		GatewayURL:    s.orchestratorURL,
		CAFilePath:    s.caCertPath,
		TokenFile:     s.enrollmentTokenFile,
		TokenEnv:      "ENROLLMENT_TOKEN",
		CustomerID:    s.customerID,
		ClusterID:     s.clusterID,
		OperatorName:  s.operatorName,
		IdentityStore: commandStore,
		Logger:        logger,
	})
	if err != nil {
		return fmt.Errorf("operator bootstrap: %w", err)
	}
	identity := result.Identity

	httpClient, err := agentTLSClient(identity.CertificatePEM, identity.PrivateKeyPEM, s.caCertPath)
	if err != nil {
		return fmt.Errorf("build agent mTLS client: %w", err)
	}
	operatorClient := operatorv1connect.NewOperatorServiceClient(httpClient, s.orchestratorURL)

	engine := helmengine.NewRealEngine(s.kubeConfig, logger)
	secretClient, err := newSecretClient(s.kubeConfig)
	if err != nil {
		return fmt.Errorf("create Kubernetes secret client: %w", err)
	}
	kubernetesClient, err := operator.NewKubernetesClient(s.kubeConfig)
	if err != nil {
		return fmt.Errorf("create emergency Kubernetes client: %w", err)
	}
	secretLister, err := secretmetadata.NewForKubeConfig(s.kubeConfig)
	if err != nil {
		return fmt.Errorf("create secret metadata lister: %w", err)
	}

	orchClient := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, s.orchestratorURL)
	s.syncer = operator.NewInventorySyncer(
		engine,
		orchClient,
		identity.OperatorID,
		identity.CustomerID,
		identity.ClusterID,
		logger,
	)
	s.agent, err = operatoragent.New(operatoragent.Config{
		Client:            operatoragent.ConnectClient{Client: operatorClient},
		Engine:            engine,
		Store:             commandStore,
		Notifier:          s.syncer,
		SyncExecutor:      s.syncer,
		Secrets:           secretClient.CoreV1(),
		EmergencyExecutor: operator.NewEmergencyCommandExecutor(kubernetesClient),
		SecretLister:      secretLister,
		SessionID:         identity.SessionID,
		OperatorID:        identity.OperatorID,
		Logger:            logger,
		InstallFlags: operatoragent.InstallFlags{
			Atomic:  s.installAtomic,
			Timeout: s.installTimeout,
		},
	})
	if err != nil {
		return fmt.Errorf("create operator agent: %w", err)
	}

	logger.Info("operator agent wired",
		"operator_id", identity.OperatorID,
		"cluster_id", identity.ClusterID,
		"gateway_url", s.orchestratorURL,
	)
	return nil
}

// agentTLSClient builds the mTLS HTTP client: the enrolled identity
// certificate as client credential and the gateway CA as the trust anchor.
func agentTLSClient(certPEM, keyPEM, caCertPath string) (*http.Client, error) {
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("load identity certificate: %w", err)
	}
	pool := x509.NewCertPool()
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read gateway CA: %w", err)
	}
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("gateway CA file contains no certificates")
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			RootCAs:      pool,
			MinVersion:   tls.VersionTLS13,
		},
		ForceAttemptHTTP2: true,
	}}, nil
}

// registerGateway keeps the management-plane operator deployment:
// authoritative store, OperatorService handler, and inventory sync when an
// identity is configured (TASK-075 plan v1 Step 7 (d); TASK-065 removes it).
func (s *operatorSvc) registerGateway(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	s.st = st
	logger.Info("store opened", "db", s.dbPath)

	s.auditEmitter = audit.NewEmitter(st.AuditEvents(), logger, audit.DefaultConfig())
	svc, err := operator.NewService(st, logger, operator.WithAudit(s.auditEmitter))
	if err != nil {
		return fmt.Errorf("create operator service: %w", err)
	}
	path, handler := operatorv1connect.NewOperatorServiceHandler(
		svc,
		connect.WithInterceptors(
			contractsinterceptor.NewRequestIDInterceptor(logger),
			contractsinterceptor.NewErrorSanitizeInterceptor(logger),
		),
	)
	mux.Handle(path, handler)

	if s.orchestratorURL == "" {
		return nil
	}
	orchClient := orchestratorv1connect.NewOrchestratorServiceClient(http.DefaultClient, s.orchestratorURL)
	// The gateway operator has no bootstrap identity; the inventory syncer
	// needs an operator_id, so it is wired only when one is configured.
	if s.customerID != "" && s.clusterID != "" {
		engine := helmengine.NewRealEngine(s.kubeConfig, logger)
		s.syncer = operator.NewInventorySyncer(
			engine,
			orchClient,
			s.operatorName, // gateway deployments identify by operator name
			s.customerID,
			s.clusterID,
			logger,
		)
	}
	logger.Info("operator gateway runtime wired", "orchestrator_url", s.orchestratorURL)
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
		restConfig *rest.Config
		err    error
	)
	if kubeConfig != "" {
		restConfig, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
	} else {
		restConfig, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("load Kubernetes REST config: %w", err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("initialize Kubernetes clientset: %w", err)
	}
	return client, nil
}

func main() {
	configPath := flag.String("config", "configs/operator.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/operator.db", "path to SQLite database (gateway mode)")
	commandDBPath := flag.String("command-db", "data/operator-commands.db", "path to durable command and identity database")
	orchestratorAddr := flag.String("orchestrator-addr", "https://operator-gateway.dev.release-manager.local:30084", "agent gateway Connect URL")
	kubeConfig := flag.String("kubeconfig", "", "path to kubeconfig; empty uses in-cluster or default config")
	installAtomic := flag.Bool("install-atomic", true, "uninstall failed releases atomically")
	installTimeout := flag.Duration("install-timeout", 5*time.Minute, "default Helm install timeout")
	flag.Parse()

	app.Run(*configPath, &operatorSvc{
		dbPath:          *dbPath,
		commandDBPath:   *commandDBPath,
		orchestratorURL: *orchestratorAddr,
		kubeConfig:      *kubeConfig,
		installAtomic:   *installAtomic,
		installTimeout:  *installTimeout,
	})
}
