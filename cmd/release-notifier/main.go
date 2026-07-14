// Command release-notifier sends notifications via DingTalk and email.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"

	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/manager"
)

func main() {
	cfg, log := loadConfig()

	srv, err := manager.NewServer(cfg, log)
	if err != nil {
		log.Error(err, "failed to create notifier server")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Info("release-notifier starting", "http_addr", cfg.Server.HTTPAddr)
	if err := srv.Run(ctx); err != nil {
		log.Error(err, "server error")
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, logr.Logger) {
	zapLog, _ := zap.NewProduction()
	log := zapr.NewLogger(zapLog)

	cfgPath := os.Getenv("CONFIG_PATH")
	if cfgPath == "" {
		cfgPath = "configs/manager.example.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error(err, "failed to load config, using defaults")
		cfg = config.DefaultConfig()
	}
	return cfg, log
}
