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
}

func (s *notifierSvc) Name() string { return "release-notifier" }

func (s *notifierSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	svc := notifier.NewService(st, logger)
	path, h := notifierv1connect.NewNotifierServiceHandler(svc)
	mux.Handle(path, h)

	// Start notification consumer goroutine.
	consumerCfg := notifier.DefaultConsumerConfig()
	consumer := notifier.NewConsumer(st, logger, consumerCfg)
	// Consumer.Run is blocking; run it in a goroutine tied to the process lifetime.
	go consumer.Run(context.Background())

	return nil
}

func main() {
	configPath := flag.String("config", "configs/notifier.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/notifier.db", "path to SQLite database")
	flag.Parse()
	app.Run(*configPath, &notifierSvc{dbPath: *dbPath})
}
