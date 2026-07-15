// Package main starts the release-webhook service.
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

	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/server"
)

func main() {
	configPath := flag.String("config", "configs/webhook.dev.yaml", "path to configuration file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg, err := config.LoadService(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	readinessChecks := map[string]func() error{
		"noop": func() error { return nil },
	}

	httpSrv := server.NewHTTP(cfg.HTTPPort, readinessChecks, logger)
	grpcSrv := server.NewGRPC(cfg.GRPCPort, logger)

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

	logger.Info("release-webhook started",
		"http_port", cfg.HTTPPort,
		"grpc_port", cfg.GRPCPort,
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

	logger.Info("release-webhook stopped")
}
