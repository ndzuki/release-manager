package postgres

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/ndzuki/release-manager/internal/config"
)

// ValidateDSN validates a PostgreSQL URL without returning credentials.
func ValidateDSN(dsn string) error {
	if dsn == "" {
		return fmt.Errorf("dsn_invalid: database.dsn is required")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return fmt.Errorf("dsn_invalid: database.dsn must use postgres:// or postgresql://")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Path == "" || u.Path == "/" {
		return fmt.Errorf("dsn_invalid: database.dsn is not a valid PostgreSQL URL")
	}
	return nil
}

func poolDefaults(cfg config.DatabaseConfig) config.DatabaseConfig {
	if cfg.MaxOpenConns == 0 {
		cfg.MaxOpenConns = 25
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = 10
	}
	return cfg
}
