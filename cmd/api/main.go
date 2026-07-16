// Package main starts the release-api service.
package main

import (
	"flag"
	"log/slog"
	"net/http"

	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/handler"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type apiSvc struct {
	dbPath string
}

func (s *apiSvc) Name() string { return "release-api" }

func (s *apiSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	st, err := sqlitestore.Open(s.dbPath)
	if err != nil {
		return err
	}
	logger.Info("store opened", "db", s.dbPath)

	valsHandler := handler.NewValuesHandler(st.Values(), 0, logger)
	valsHandler.Register(mux)

	return nil
}

func main() {
	configPath := flag.String("config", "configs/api.dev.yaml", "path to config file")
	dbPath := flag.String("db", "data/api.db", "path to SQLite database")
	flag.Parse()

	app.Run(*configPath, &apiSvc{dbPath: *dbPath})
}
