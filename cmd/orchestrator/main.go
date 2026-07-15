// Package main starts the release-orchestrator service.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/orchestrator"
	"github.com/ndzuki/release-manager/internal/server"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func main() {
	configPath := flag.String("config", "configs/orchestrator.dev.yaml", "path to configuration file")
	dbPath := flag.String("db", "data/orchestrator.db", "path to SQLite database")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg, err := config.LoadService(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Open the SQLite store.
	st, err := sqlitestore.Open(*dbPath)
	if err != nil {
		logger.Error("failed to open store", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	logger.Info("store opened", "db", *dbPath)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	readinessChecks := map[string]func() error{
		"db": func() error {
			// Simple liveness: ping the database.
			return st.DB().PingContext(ctx)
		},
	}

	httpSrv := server.NewHTTP(cfg.HTTPPort, readinessChecks, logger)
	grpcSrv := server.NewGRPC(cfg.GRPCPort, logger)

	// Register the orchestrator gRPC service.
	orchSvc := orchestrator.NewService(st, logger)
	orchestratorv1.RegisterOrchestratorServiceServer(grpcSrv.Server(), orchSvc)

	errCh := make(chan error, 2)

	go func() {
		if err := httpSrv.Start(); err != nil {
			errCh <- fmt.Errorf("http: %w", err)
		}
	}()

	go func() {
		if err := grpcSrv.Start(); err != nil {
			errCh <- fmt.Errorf("grpc: %w", err)
		}
	}()

	logger.Info("release-orchestrator started",
		"http_port", cfg.HTTPPort,
		"grpc_port", cfg.GRPCPort,
		"db", *dbPath,
	)

	select {
	case err := <-errCh:
		logger.Error("server error", "error", err)
		cancel()
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "error", err)
	}
	if err := grpcSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("grpc shutdown error", "error", err)
	}

	logger.Info("release-orchestrator stopped")
}
