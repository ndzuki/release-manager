// Package app provides centralized service lifecycle management.
// Each service implements the Service interface and calls app.Run.
// app.Run creates an http.ServeMux, registers /health and /readyz,
// wires Connect handlers via Service.Register, and manages graceful shutdown.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/handler"
)

// Shutdowner releases resources after the HTTP server stops accepting requests.
type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

// Service is the interface each microservice must satisfy.
type Service interface {
	Name() string
	// Register mounts Connect handlers onto the stdlib mux.
	// /health and /readyz are already registered by app.Run.
	Register(mux *http.ServeMux, logger *slog.Logger) error
}
type configAwareService interface {
	Configure(*config.ServiceConfig)
}

type serverConfigurer interface {
	ConfigureServer(*http.Server) error
}

type backgroundService interface {
	Run(context.Context)
}

type closeService interface {
	Close() error
}

// readinessContributor is an optional interface services can implement
// to supply dependency readiness checks for /readyz.
type readinessContributor interface {
	ReadinessChecks() map[string]func() error
}

type tlsService interface {
	TLSCertificateFiles() (certFile string, keyFile string, enabled bool)
}

// Run starts a service with config loading, signal handling, and graceful shutdown.
//
//nolint:gocyclo // Service startup keeps lifecycle and shutdown gates explicit in one owner.
func Run(configPath string, svc Service) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg, err := config.LoadService(configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	if aware, ok := svc.(configAwareService); ok {
		aware.Configure(cfg)
	}

	readinessChecks := map[string]func() error{
		"noop": func() error { return nil },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health())

	if err := svc.Register(mux, logger); err != nil {
		logger.Error("failed to register service", "error", err)
		os.Exit(1)
	}

	// Collect readiness checks from the service after Register (store is open).
	if checker, ok := svc.(readinessContributor); ok {
		for k, v := range checker.ReadinessChecks() {
			readinessChecks[k] = v
		}
	}

	mux.HandleFunc("GET /readyz", handler.Ready(readinessChecks))

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if configurer, ok := svc.(serverConfigurer); ok {
		if err := configurer.ConfigureServer(srv); err != nil {
			logger.Error("failed to configure server", "error", err)
			return
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if background, ok := svc.(backgroundService); ok {
		go background.Run(ctx)
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info(svc.Name()+" started", "http_port", cfg.HTTPPort)
		var serveErr error
		if tlsConfig, ok := svc.(tlsService); ok {
			certFile, keyFile, enabled := tlsConfig.TLSCertificateFiles()
			if enabled {
				serveErr = srv.ListenAndServeTLS(certFile, keyFile)
			} else {
				serveErr = srv.ListenAndServe()
			}
		} else {
			serveErr = srv.ListenAndServe()
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- fmt.Errorf("server: %w", serveErr)
		}
	}()

	select {
	case err := <-errCh:
		logger.Error("server error", "error", err)
		cancel()
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}
	if shutdowner, ok := svc.(Shutdowner); ok {
		if err := shutdowner.Shutdown(shutdownCtx); err != nil {
			logger.Error("service shutdown error", "error", err)
		}
	}

	if closer, ok := svc.(closeService); ok {
		if err := closer.Close(); err != nil {
			logger.Error("service close error", "error", err)
		}
	}
	logger.Info(svc.Name() + " stopped")
}
