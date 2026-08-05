package config

import (
	"os"
	"path/filepath"
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

func TestLoadServiceRedisConfiguration(t *testing.T) {
	t.Run("yaml", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
database:
  driver: sqlite
  dsn: auth.db
redis:
  address: redis:6379
  password: secret
  db: 3
`), 0o600))

		cfg, err := LoadService(path)
		require.NoError(t, err)
		require.Equal(t, "redis:6379", cfg.Redis.Address)
		require.Equal(t, "secret", cfg.Redis.Password)
		require.Equal(t, 3, cfg.Redis.DB)
	})

	t.Run("environment overrides", func(t *testing.T) {
		t.Setenv("REDIS_ADDRESS", "redis.example:6380")
		t.Setenv("REDIS_PASSWORD", "env-secret")
		t.Setenv("REDIS_DB", "5")

		cfg, err := LoadService("../../configs/auth.dev.yaml")
		require.NoError(t, err)
		require.Equal(t, "redis.example:6380", cfg.Redis.Address)
		require.Equal(t, "env-secret", cfg.Redis.Password)
		require.Equal(t, 5, cfg.Redis.DB)
	})
}
