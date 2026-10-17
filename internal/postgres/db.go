// Package postgres provides the shared PostgreSQL database infrastructure.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/ndzuki/release-manager/internal/config"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Database owns one physical database/sql pool and a GORM wrapper over it.
type Database struct {
	sqlDB    *sql.DB
	gorm     *gorm.DB
	once     sync.Once
	closeErr error
}

// Open validates configuration, opens the shared pgx stdlib pool, waits for the
// server to answer, and wraps the same pool with GORM. It never logs or returns
// the DSN.
//
// The wait is bounded and only retries transport-level failures (TASK-351): a
// pod that starts before kube-router has admitted its address to the postgres
// NetworkPolicy gets ECONNREFUSED for a few hundred milliseconds, and a single
// ping would turn that window into exit 1 plus a CrashLoopBackOff restart. A
// server that answers with an error is reported without retrying.
func Open(ctx context.Context, cfg config.DatabaseConfig) (*Database, error) {
	return open(ctx, cfg, defaultConnectPolicy, slog.Default(), sleepCtx, jitterDelay)
}

// open is Open with the startup-wait policy injected, so tests can pin the
// retry decision without a database or real delays.
func open(
	ctx context.Context,
	cfg config.DatabaseConfig,
	policy connectPolicy,
	logger *slog.Logger,
	sleep sleepFunc,
	jitter jitterFunc,
) (*Database, error) {
	if cfg.Driver == "" {
		cfg.Driver = "postgres"
	}
	if cfg.Driver != "postgres" {
		return nil, fmt.Errorf("dsn_invalid: PostgreSQL database requires database.driver=postgres")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := ValidateDSN(cfg.DSN); err != nil {
		return nil, err
	}
	cfg = poolDefaults(cfg)
	connConfig, err := parseDSN(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("dsn_invalid: database.dsn is not a valid PostgreSQL URL")
	}
	db := stdlib.OpenDB(*connConfig)
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	if err := waitForServer(ctx, db.PingContext, policy, logger, sleep, jitter); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connection_unavailable: ping PostgreSQL: %w", err)
	}

	gormDB, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connection_unavailable: initialize GORM: %w", err)
	}
	return &Database{sqlDB: db, gorm: gormDB}, nil
}

func parseDSN(dsn string) (*pgx.ConnConfig, error) {
	return pgx.ParseConfig(dsn)
}

// SQLDB returns the shared database/sql pool used by GORM and raw SQL.
func (d *Database) SQLDB() *sql.DB { return d.sqlDB }

// GORM returns the GORM wrapper over SQLDB.
func (d *Database) GORM() *gorm.DB { return d.gorm }

// Close is idempotent.
func (d *Database) Close() error {
	if d == nil || d.sqlDB == nil {
		return nil
	}
	d.once.Do(func() { d.closeErr = d.sqlDB.Close() })
	return d.closeErr
}
