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

// TASK-388: an unset gateway.hostnames must keep the exact pre-change SAN pair,
// so the fix widens the certificate contract without moving any existing
// deployment's server certificate.
func TestGatewayWithDefaultsKeepsLegacyHostnames(t *testing.T) {
	t.Run("empty hostnames keep the legacy pair", func(t *testing.T) {
		cfg := (GatewayCfg{}).WithDefaults()
		require.Equal(t, []string{
			"operator-gateway.dev.release-manager.local",
			"localhost",
		}, cfg.Hostnames)
		require.Empty(t, cfg.IPAddresses, "no IP SAN may appear unless configured")
		require.Equal(t, 8084, cfg.Port)
	})

	t.Run("configured hostnames win", func(t *testing.T) {
		cfg := (GatewayCfg{Hostnames: []string{"rm-prod.local"}}).WithDefaults()
		require.Equal(t, []string{"rm-prod.local"}, cfg.Hostnames,
			"configuration must replace the defaults, not extend them")
	})

	t.Run("defaults are not aliased across calls", func(t *testing.T) {
		first := (GatewayCfg{}).WithDefaults()
		first.Hostnames[0] = "mutated"
		require.Equal(t, "operator-gateway.dev.release-manager.local",
			(GatewayCfg{}).WithDefaults().Hostnames[0])
	})
}

// TASK-388: SAN values are verified by customer agents, so a malformed entry
// must fail closed at load time instead of producing an unverifiable
// certificate.
func TestGatewayValidateSANs(t *testing.T) {
	tests := []struct {
		name    string
		cfg     GatewayCfg
		wantErr string
	}{
		{name: "zero value uses the valid defaults"},
		{name: "valid IP v4 and v6", cfg: GatewayCfg{
			Hostnames:   []string{"rm-prod.local"},
			IPAddresses: []string{"10.10.33.122", "2001:db8::1"},
		}},
		{name: "invalid IP", cfg: GatewayCfg{IPAddresses: []string{"10.10.33"}}, wantErr: "gateway.ip_addresses"},
		{name: "hostname that is an IP is not an IP SAN but stays a valid DNS name", cfg: GatewayCfg{
			Hostnames: []string{"10.10.33.122"},
		}},
		{name: "empty hostname", cfg: GatewayCfg{Hostnames: []string{""}}, wantErr: "gateway.hostnames"},
		{name: "whitespace-only hostname", cfg: GatewayCfg{Hostnames: []string{"   "}}, wantErr: "gateway.hostnames"},
		{name: "hostname with a space", cfg: GatewayCfg{Hostnames: []string{"rm prod.local"}}, wantErr: "gateway.hostnames"},
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

// TASK-388: the YAML keys (and their env overrides) must reach the struct the
// gateway certificate is built from. The env form is comma-separated, matching
// the existing values.secret_patterns slice binding.
func TestLoadServiceDecodesGatewaySANs(t *testing.T) {
	t.Run("yaml and environment", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "orchestrator.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
gateway:
  enabled: true
  port: 8084
  hostnames:
    - rm-prod.local
  ip_addresses:
    - 10.10.33.122
`), 0o600))

		cfg, err := LoadService(path)
		require.NoError(t, err)
		require.Equal(t, []string{"rm-prod.local"}, cfg.Gateway.Hostnames)
		require.Equal(t, []string{"10.10.33.122"}, cfg.Gateway.IPAddresses)

		t.Setenv("GATEWAY_HOSTNAMES", "rm-prod.local,gateway.internal")
		t.Setenv("GATEWAY_IP_ADDRESSES", "10.10.33.122,2001:db8::1")
		cfg, err = LoadService(path)
		require.NoError(t, err)
		require.Equal(t, []string{"rm-prod.local", "gateway.internal"}, cfg.Gateway.Hostnames)
		require.Equal(t, []string{"10.10.33.122", "2001:db8::1"}, cfg.Gateway.IPAddresses)
	})

	t.Run("invalid IP fails the load", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "orchestrator.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
gateway:
  ip_addresses:
    - 10.10.33.999
`), 0o600))
		_, err := LoadService(path)
		require.ErrorContains(t, err, "gateway_invalid")
	})
}

// TASK-388 delivery contract: the production overlay must actually publish the
// customer-facing IP SAN and a leaf lifetime that survives a long-running
// orchestrator. A regression here silently ships a certificate customer agents
// reject.
func TestProductionConfigPublishesGatewaySANs(t *testing.T) {
	cfg, err := LoadService("../../deploy/kustomize/production/configs/orchestrator.production.yaml")
	require.NoError(t, err)
	require.Contains(t, cfg.Gateway.Hostnames, "rm-prod.local")
	require.Contains(t, cfg.Gateway.IPAddresses, "10.10.33.122")
	require.Equal(t, 365*24*time.Hour, cfg.CA.CertTTL)
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
