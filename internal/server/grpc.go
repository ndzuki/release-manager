// Package server provides gRPC server lifecycle management.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// GRPCServer wraps the gRPC server for the manager service.
type GRPCServer struct {
	srv    *grpc.Server
	port   int
	logger *slog.Logger
}

// NewGRPC creates a new gRPC server.
func NewGRPC(port int, logger *slog.Logger) *GRPCServer {
	srv := grpc.NewServer()
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv, hs)
	reflection.Register(srv)

	return &GRPCServer{
		srv:    srv,
		port:   port,
		logger: logger,
	}
}

// Server returns the underlying gRPC server for service registration.
func (s *GRPCServer) Server() *grpc.Server { return s.srv }

// Start begins listening and blocks until the server stops.
func (s *GRPCServer) Start() error {
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return fmt.Errorf("grpc listen: %w", err)
	}
	s.logger.Info("starting gRPC server", "port", s.port)
	if err := s.srv.Serve(lis); err != nil {
		return fmt.Errorf("grpc serve: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the gRPC server.
func (s *GRPCServer) Shutdown(_ context.Context) error {
	s.logger.Info("shutting down gRPC server")
	s.srv.GracefulStop()
	return nil
}
