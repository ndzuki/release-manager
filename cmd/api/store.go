package main

import (
	"context"
	"io/fs"
	"os"

	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/migrations"
)

// databaseConfigFromService returns the database section of the service config.
// The nil branch is only reachable from tests that call Register directly:
// app.Run loads the same file first and exits on error. The engine selector is
// still read from DATABASE_DRIVER/DATABASE_DSN there because that is the same
// environment contract the other services use, so an explicit DATABASE_DRIVER
// can never silently degrade to SQLite.
func databaseConfigFromService(cfg *config.ServiceConfig) config.DatabaseConfig {
	if cfg != nil {
		return cfg.Database
	}
	return config.DatabaseConfig{
		Driver: os.Getenv("DATABASE_DRIVER"),
		DSN:    os.Getenv("DATABASE_DSN"),
	}
}

// storeConstructors is the pair of engine constructors openStore dispatches to.
// The indirection exists so the engine choice is testable without a database:
// postgresstore.Open dials and migrates a real server, so a test cannot otherwise
// observe which constructor a driver selected. Production always dispatches
// through productionStoreConstructors, which are the real constructors, so this
// changes no behavior.
type storeConstructors struct {
	sqlite   func(dsn string) (store.Store, error)
	postgres func(ctx context.Context, cfg config.DatabaseConfig, migrationFS fs.FS) (store.Store, error)
}

var productionStoreConstructors = storeConstructors{
	sqlite: func(dsn string) (store.Store, error) {
		return sqlitestore.Open(dsn)
	},
	postgres: func(ctx context.Context, cfg config.DatabaseConfig, migrationFS fs.FS) (store.Store, error) {
		return postgresstore.Open(ctx, cfg, migrationFS)
	},
}

// openStore selects the persistence engine the audit surface reads and writes.
// database.driver (bound from DATABASE_DRIVER) defaults to SQLite, which keeps
// the long-standing --db file authoritative for dev/test; postgres routes the
// audit query, emit and archive paths at the same tables auth and orchestrator
// write (TASK-338). Both engines implement store.Store, so this is a
// constructor choice rather than an adapter. Startup runs on the process
// context, the same shape as cmd/auth and cmd/orchestrator.
func openStore(sqlitePath string, cfg config.DatabaseConfig) (store.Store, error) {
	return openStoreWith(productionStoreConstructors, sqlitePath, cfg)
}

// openStoreWith is openStore with the constructors injected. The selection and
// validation rules live here, so tests can pin the routing decision itself
// (TASK-338) instead of the engine's network I/O.
func openStoreWith(ctors storeConstructors, sqlitePath string, cfg config.DatabaseConfig) (store.Store, error) {
	if cfg.Driver == "" {
		return ctors.sqlite(sqlitePath)
	}
	if cfg.Driver == "sqlite" && cfg.DSN == "" {
		// --db has always been this service's SQLite connection string; keep it
		// authoritative when a config selects SQLite without naming another file.
		cfg.DSN = sqlitePath
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Driver == "postgres" {
		// Same constructor as auth/orchestrator: shared pool plus the embedded
		// migrations (ADR-070, migrations/embed.go).
		return ctors.postgres(context.Background(), cfg, migrations.FS)
	}
	return ctors.sqlite(cfg.DSN)
}

// storePing returns the opened engine's connection ping for /readyz (TASK-352).
// The concrete engines are the only owners of the *sql.DB handle, so the type
// switch mirrors cmd/auth and cmd/orchestrator instead of widening
// store.Store. A nil result leaves the service with only the noop check instead
// of silently reporting a database it cannot ping as ready.
func storePing(st store.Store) func(context.Context) error {
	switch st := st.(type) {
	case *postgresstore.Store:
		return st.SQLDB().PingContext
	case *sqlitestore.Store:
		return st.DB().PingContext
	}
	return nil
}

// storeLogAttrs describes the opened engine without ever logging a PostgreSQL
// DSN, which embeds the password. The SQLite DSN is a plain file path.
func storeLogAttrs(cfg config.DatabaseConfig, sqlitePath string) []any {
	switch cfg.Driver {
	case "postgres":
		return []any{"driver", "postgres"}
	case "sqlite":
		dsn := cfg.DSN
		if dsn == "" {
			dsn = sqlitePath
		}
		return []any{"driver", "sqlite", "db", dsn}
	default:
		return []any{"driver", "sqlite", "db", sqlitePath}
	}
}
