// Package main starts the release-notifier service.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"

	notifierv1connect "github.com/ndzuki/release-manager/api/gen/notifier/v1/notifierv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/notifier"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type notifierSvc struct {
	dbPath string

	// consumer lifecycle
	consumer       *notifier.Consumer
	consumerCancel context.CancelFunc
}

func (s *notifierSvc) Name() string { return "release-notifier" }

func (s *notifierSvc) Shutdown(ctx context.Context) error {
	if s.consumerCancel != nil {
		s.consumerCancel()
	}
	return nil
}

func (s *notifierSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	svc := notifier.NewService(st, logger)
	path, h := notifierv1connect.NewNotifierServiceHandler(svc)
	mux.Handle(path, h)

	// Build sender. In production this would use a real SecretResolver.
	// For now, webhook sender without secret resolution; unconfigured
	// channels are rejected at Send time.
	sender := notifier.NewWebhookSender(nil)

	consumerCfg := notifier.DefaultConsumerConfig()
	s.consumer = notifier.NewConsumer(
		st.Notifications(),
		sender,
		logger,
		consumerCfg,
	)

	// Consumer runs within app.Run lifecycle; cancellation triggers clean shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	s.consumerCancel = cancel
	go s.consumer.Run(ctx)

	return nil
}

func main() {
	configPath := flag.String("config", "configs/notifier.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/notifier.db", "path to SQLite database")
	flag.Parse()
	app.Run(*configPath, &notifierSvc{dbPath: *dbPath})
}
