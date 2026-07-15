package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ndzuki/release-manager/internal/handler"
)

// HTTPServer wraps the HTTP server for the manager service.
type HTTPServer struct {
	srv    *http.Server
	port   int
	logger *slog.Logger
}

// NewHTTP creates a new HTTP server with the given configuration.
func NewHTTP(port int, readinessChecks map[string]func() error, logger *slog.Logger) *HTTPServer {
	r := chi.NewRouter()

	r.Get("/health", handler.Health())
	r.Get("/readyz", handler.Ready(readinessChecks))

	return &HTTPServer{
		srv: &http.Server{
			Addr:              fmt.Sprintf(":%d", port),
			Handler:           r,
			ReadHeaderTimeout: 10 * time.Second,
		},
		port:   port,
		logger: logger,
	}
}

// Start begins listening and blocks until the server stops.
func (s *HTTPServer) Start() error {
	s.logger.Info("starting HTTP server", "port", s.port)
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http serve: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the HTTP server.
func (s *HTTPServer) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down HTTP server")
	return s.srv.Shutdown(ctx)
}
