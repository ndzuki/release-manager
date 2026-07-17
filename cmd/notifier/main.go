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
	dbPath       string
	store        *sqlitestore.Store
	consumer     *notifier.Consumer
	consumerDone chan struct{}
}

func (s *notifierSvc) Name() string { return "release-notifier" }

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

func (s *notifierSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	s.store = st
	s.consumerDone = make(chan struct{})
	logger.Info("store opened", "db", s.dbPath)

	svc := notifier.NewService(st, logger)
	path, h := notifierv1connect.NewNotifierServiceHandler(svc)
	mux.Handle(path, h)

	// Unconfigured channels are rejected during delivery.
	sender := notifier.NewWebhookSender(nil)

	consumerCfg := notifier.DefaultConsumerConfig()
	s.consumer = notifier.NewConsumer(
		st.Notifications(),
		sender,
		logger,
		consumerCfg,
	)

	return nil
}

func main() {
	configPath := flag.String("config", "configs/notifier.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/notifier.db", "path to SQLite database")
	flag.Parse()
	app.Run(*configPath, &notifierSvc{dbPath: *dbPath})
}
