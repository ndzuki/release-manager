package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

func TestValuesConfigWithDefaults(t *testing.T) {
	defaults := (ValuesConfig{}).WithDefaults()
	require.Equal(t, int64(1<<20), defaults.MaxDocumentBytes)
	require.Empty(t, defaults.SecretPatterns)

	configured := (ValuesConfig{MaxDocumentBytes: 2048, SecretPatterns: []string{"credential"}}).WithDefaults()
	require.Equal(t, int64(2048), configured.MaxDocumentBytes)
	require.Equal(t, []string{"credential"}, configured.SecretPatterns)
}

// D-N: the terminal-notification worker is enabled only when it has both an
// address to call and a recipient to send to; either alone leaves it disabled
// rather than acknowledging entries that were never sent.
func TestNotifierDeliveryDefaultsAndEnabled(t *testing.T) {
	zero := NotifierCfg{}
	assert.False(t, zero.DeliveryEnabled(), "no url and no recipient is disabled")

	assert.False(t, NotifierCfg{URL: "http://notifier:8086"}.DeliveryEnabled(), "url without a recipient is disabled")
	assert.False(t, NotifierCfg{Recipient: "https://hooks.example/ops"}.DeliveryEnabled(), "recipient without a url is disabled")
	assert.True(t, NotifierCfg{URL: "http://notifier:8086", Recipient: "https://hooks.example/ops"}.DeliveryEnabled())

	defaulted := NotifierCfg{URL: "http://notifier:8086", Recipient: "https://hooks.example/ops"}.WithDeliveryDefaults()
	assert.Equal(t, "webhook", defaulted.DeliveryChannel)
	assert.Equal(t, 30*time.Second, defaulted.PollInterval)

	custom := NotifierCfg{DeliveryChannel: "slack", PollInterval: time.Minute}.WithDeliveryDefaults()
	assert.Equal(t, "slack", custom.DeliveryChannel)
	assert.Equal(t, time.Minute, custom.PollInterval)
}
