package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabaseConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     DatabaseConfig
		wantErr string
	}{
		{name: "missing driver", wantErr: "database.driver"},
		{name: "missing postgres dsn", cfg: DatabaseConfig{Driver: "postgres"}, wantErr: "dsn_invalid"},
		{name: "postgres with sqlite dsn", cfg: DatabaseConfig{Driver: "postgres", DSN: "file:test.db"}, wantErr: "dsn_invalid"},
		{name: "valid postgres", cfg: DatabaseConfig{Driver: "postgres", DSN: "postgres://user:pass@localhost:5432/release?sslmode=disable"}},
		{name: "valid sqlite", cfg: DatabaseConfig{Driver: "sqlite", DSN: "data/orchestrator.db"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestLoadServiceDatabaseEnvironmentOverrides(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "postgres")
	t.Setenv("DATABASE_DSN", "postgres://user:pass@postgres:5432/release?sslmode=disable")
	t.Setenv("MAINTENANCE", "true")

	cfg, err := LoadService("../../configs/orchestrator.dev.yaml")
	require.NoError(t, err)
	require.Equal(t, "postgres", cfg.Database.Driver)
	require.Equal(t, "postgres://user:pass@postgres:5432/release?sslmode=disable", cfg.Database.DSN)
	require.True(t, cfg.Maintenance)
}
