// Package main starts the release-operator service.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"connectrpc.com/connect"

	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type operatorSvc struct {
	dbPath          string
	orchestratorURL string
	clientConfig    operator.SessionClientConfig
	st              *sqlitestore.Store
	svc             *operator.Service
	sessionClient   *operator.SessionClient
	inventorySyncer *operator.InventorySyncer
	serverCertFile  string
	serverKeyFile   string
	clientCAFile    string
}

func (s *operatorSvc) Name() string { return "release-operator" }

func (s *operatorSvc) TLSCertificateFiles() (
	certFile string,
	keyFile string,
	enabled bool,
) {
	return s.serverCertFile,
		s.serverKeyFile,
		s.serverCertFile != "" && s.serverKeyFile != "" && s.clientCAFile != ""
}

func (s *operatorSvc) ConfigureServer(server *http.Server) error {
	_, _, enabled := s.TLSCertificateFiles()
	if !enabled {
		return nil
	}
	caPEM, err := os.ReadFile(s.clientCAFile)
	if err != nil {
		return fmt.Errorf("read client ca: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("parse client ca")
	}
	server.TLSConfig = &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  clientCAs,
	}
	return nil
}

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
	s.svc = svc
	path, h := operatorv1connect.NewOperatorServiceHandler(
		svc,
		connect.WithInterceptors(),
	)
	mux.Handle(path, operator.NewCertificateIdentityHandler(h))

	if s.orchestratorURL != "" {
		orchClient := orchestratorv1connect.NewOrchestratorServiceClient(
			http.DefaultClient,
			s.orchestratorURL,
		)
		engine := helmengine.NewFake()
		s.inventorySyncer = operator.NewInventorySyncer(
			engine, orchClient,
			"", "", "", // operator_id, customer_id, cluster_id set on enrollment
			logger,
		)
		svc.SetInventorySyncer(s.inventorySyncer)
		logger.Info("inventory syncer wired", "orchestrator_url", s.orchestratorURL)
	}

	credentialsConfigured := len(s.clientConfig.Certificate.Certificate) > 0 && s.clientConfig.RootCAs != nil
	identityConfigured := s.clientConfig.OperatorID != "" && s.clientConfig.InstanceID != "" && s.clientConfig.Version != ""
	if s.clientConfig.BaseURL != "" && credentialsConfigured && identityConfigured {
		s.sessionClient, err = operator.NewSessionClient(s.clientConfig, logger)
		if err != nil {
			return fmt.Errorf("create session client: %w", err)
		}
	}
	return nil
}

func (s *operatorSvc) Run(ctx context.Context) {
	if s.inventorySyncer != nil {
		s.inventorySyncer.Start(ctx)
	}
	if s.sessionClient != nil {
		go func() {
			if err := s.sessionClient.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("session client stopped", "error", err)
			}
		}()
	}
	if s.svc != nil {
		s.svc.RunSessionMonitor(ctx)
	}
}

func (s *operatorSvc) Close() error {
	if s.st == nil {
		return nil
	}
	return s.st.Close()
}

func loadClientCredentials(
	certFile string,
	keyFile string,
	caFile string,
) (tls.Certificate, *x509.CertPool, error) {
	if certFile == "" && keyFile == "" && caFile == "" {
		return tls.Certificate{}, nil, nil
	}
	if certFile == "" || keyFile == "" || caFile == "" {
		return tls.Certificate{}, nil, fmt.Errorf("client cert, key, and server ca are required together")
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load client certificate: %w", err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read server ca: %w", err)
	}
	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, fmt.Errorf("parse server ca")
	}
	return certificate, rootCAs, nil
}

func main() {
	configPath := flag.String("config", "configs/operator.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/operator.db", "path to SQLite database")
	orchestratorAddr := flag.String("orchestrator-addr", "http://localhost:8081", "orchestrator Connect URL")
	operatorID := flag.String("operator-id", "", "logical operator id")
	instanceID := flag.String("instance-id", "", "operator instance id")
	clientCertFile := flag.String("client-cert", "", "operator mTLS certificate file")
	clientKeyFile := flag.String("client-key", "", "operator mTLS private key file")
	serverCAFile := flag.String("server-ca", "", "trusted control-plane CA file")
	serverCertFile := flag.String("tls-cert", "", "server TLS certificate file")
	serverKeyFile := flag.String("tls-key", "", "server TLS private key file")
	clientCAFile := flag.String("client-ca", "", "trusted operator client CA file")
	operatorVersion := flag.String("operator-version", "", "operator version")
	flag.Parse()
	clientCertificate, rootCAs, err := loadClientCredentials(*clientCertFile, *clientKeyFile, *serverCAFile)
	if err != nil {
		panic(err)
	}

	app.Run(*configPath, &operatorSvc{
		dbPath:          *dbPath,
		orchestratorURL: *orchestratorAddr,
		serverCertFile:  *serverCertFile,
		serverKeyFile:   *serverKeyFile,
		clientCAFile:    *clientCAFile,
		clientConfig: operator.SessionClientConfig{
			BaseURL:     *orchestratorAddr,
			OperatorID:  *operatorID,
			InstanceID:  *instanceID,
			Version:     *operatorVersion,
			Certificate: clientCertificate,
			RootCAs:     rootCAs,
		},
	})
}
