// Package main starts the release-notifier service.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	notifierv1connect "github.com/ndzuki/release-manager/api/gen/notifier/v1/notifierv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/config"
	contractsinterceptor "github.com/ndzuki/release-manager/internal/contracts/interceptor"
	"github.com/ndzuki/release-manager/internal/notifier"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/migrations"
)

type notifierSvc struct {
	cfg          config.ServiceConfig
	store        store.Store
	consumer     *notifier.Consumer
	consumerDone chan struct{}
	pingDB       func(context.Context) error
}

func (s *notifierSvc) Name() string { return "release-notifier" }

func (s *notifierSvc) Configure(cfg *config.ServiceConfig) { s.cfg = *cfg }

func (s *notifierSvc) Close() error {
	if s.consumerDone != nil {
		<-s.consumerDone
	}
	if s.store == nil {
		return nil
	}
	return s.store.Close()
}

func (s *notifierSvc) Run(ctx context.Context) {
	defer close(s.consumerDone)
	s.consumer.Run(ctx)
}

func (s *notifierSvc) ReadinessChecks() map[string]func() error {
	if s.pingDB == nil {
		return nil
	}
	return map[string]func() error{
		"database": func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return s.pingDB(ctx)
		},
	}
}

func (s *notifierSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	if err := s.openStore(); err != nil {
		return err
	}
	s.consumerDone = make(chan struct{})
	logger.Info("store opened", "driver", s.cfg.Database.Driver)

	svc := notifier.NewService(s.store, logger)
	path, h := notifierv1connect.NewNotifierServiceHandler(
		svc,
		connect.WithInterceptors(
			contractsinterceptor.NewRequestIDInterceptor(logger),
			contractsinterceptor.NewErrorSanitizeInterceptor(logger),
		),
	)
	mux.Handle(path, h)

	// Unconfigured channels are rejected during delivery.
	sender := notifier.NewWebhookSender(nil)

	consumerCfg := notifier.DefaultConsumerConfig()
	s.consumer = notifier.NewConsumer(
		s.store.Notifications(),
		sender,
		logger,
		consumerCfg,
	)

	return nil
}

// openStore connects the configured backend (ADR-015: exactly one authority
// per process). The postgres path opens the per-authority release_notifier
// database and runs its golang-migrate migrations; any migration failure
// aborts startup (REQ-070). The notifier only uses Notifications(); the
// release_notifier database has no other tables, so any other accessor would
// fail fast with a SQL error.
func (s *notifierSvc) openStore() error {
	if err := s.cfg.Database.Validate(); err != nil {
		return err
	}
	var err error
	switch s.cfg.Database.Driver {
	case "postgres":
		s.store, err = postgresstore.Open(context.Background(), s.cfg.Database, migrations.ReleaseNotifierFS())
	case "sqlite":
		s.store, err = sqlitestore.Open(s.cfg.Database.DSN)
	}
	if err != nil {
		return err
	}
	switch backend := s.store.(type) {
	case *postgresstore.Store:
		s.pingDB = backend.SQLDB().PingContext
	case *sqlitestore.Store:
		s.pingDB = backend.DB().PingContext
	}
	return nil
}

func main() {
	configPath := flag.String("config", "configs/notifier.dev.yaml", "path to config file")
	flag.Parse()
	app.Run(*configPath, &notifierSvc{})
}
