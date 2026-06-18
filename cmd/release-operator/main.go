// Package main is the entry point for release-operator.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/pkg/log"
)

func main() {
	configPath := flag.String("config", "", "path to config file (YAML)")
	customerID := flag.String("customer-id", "", "customer identifier (required)")
	notificationEndpoint := flag.String("notification-endpoint", "", "release-manager gRPC endpoint")
	flag.Parse()

	if *customerID == "" {
		*customerID = os.Getenv("CUSTOMER_ID")
		if *customerID == "" {
			fmt.Fprintf(os.Stderr, "customer-id is required (use --customer-id or CUSTOMER_ID env)\n")
			os.Exit(1)
		}
	}

	if *notificationEndpoint == "" {
		*notificationEndpoint = os.Getenv("NOTIFICATION_ENDPOINT")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Config file can override the flag/env
	if cfg.NotificationEndpoint != "" && *notificationEndpoint == "" {
		*notificationEndpoint = cfg.NotificationEndpoint
	}

	if *notificationEndpoint == "" {
		fmt.Fprintf(os.Stderr, "notification-endpoint is required (use --notification-endpoint, NOTIFICATION_ENDPOINT env, or config file)\n")
		os.Exit(1)
	}

	logger, err := log.New(cfg.Log.Level, cfg.Log.Format, cfg.Log.Output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	logger.Info("starting release-operator",
		"customer_id", *customerID,
		"grpc_addr", cfg.Server.GRPCAddr,
		"notification_endpoint", *notificationEndpoint,
	)

	srv, err := operator.NewServer(cfg, *customerID, *notificationEndpoint, logger)
	if err != nil {
		logger.Error(err, "failed to create operator server")
		os.Exit(1)
	}

	if err := srv.Run(context.Background()); err != nil {
		logger.Error(err, "operator server exited with error")
		os.Exit(1)
	}

	logger.Info("release-operator stopped")
}
