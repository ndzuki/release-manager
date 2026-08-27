// Package config loads release-manager configuration.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

// Config holds the service configuration.
type Config struct {
	HTTPPort             int               `mapstructure:"http_port"`
	LogLevel             string            `mapstructure:"log_level"`
	RuntimePullPreflight RuntimePullConfig `mapstructure:"runtime_pull_preflight"`
	Database             DatabaseConfig    `mapstructure:"database"`
	Maintenance          bool              `mapstructure:"maintenance"`
	Values               ValuesConfig      `mapstructure:"values"`
}

// DatabaseConfig describes the authoritative application database.
type DatabaseConfig struct {
	Driver          string        `mapstructure:"driver"`
	DSN             string        `mapstructure:"dsn"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `mapstructure:"conn_max_idle_time"`
}

// RedisConfig describes the optional auth session cache and blacklist service.
type RedisConfig struct {
	Address  string `mapstructure:"address"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// Validate checks the selected connection settings without exposing secrets.
func (c DatabaseConfig) Validate() error {
	switch c.Driver {
	case "postgres":
		if c.DSN == "" {
			return fmt.Errorf("dsn_invalid: database.dsn is required")
		}
		if !strings.HasPrefix(c.DSN, "postgres://") && !strings.HasPrefix(c.DSN, "postgresql://") {
			return fmt.Errorf("dsn_invalid: database.dsn must use postgres:// or postgresql://")
		}
	case "sqlite":
		if strings.TrimSpace(c.DSN) == "" {
			return fmt.Errorf("dsn_invalid: database.dsn is required")
		}
	default:
		return fmt.Errorf("dsn_invalid: database.driver must be postgres or sqlite")
	}
	if c.MaxOpenConns < 0 || c.MaxIdleConns < 0 || c.MaxIdleConns > c.MaxOpenConns && c.MaxOpenConns > 0 {
		return fmt.Errorf("dsn_invalid: invalid connection pool limits")
	}
	if c.ConnMaxLifetime < 0 || c.ConnMaxIdleTime < 0 {
		return fmt.Errorf("dsn_invalid: connection lifetimes must not be negative")
	}
	return nil
}

type RuntimePullConfig struct {
	Enabled        bool          `mapstructure:"enabled"`
	Namespace      string        `mapstructure:"namespace"`
	ServiceAccount string        `mapstructure:"service_account"`
	Timeout        time.Duration `mapstructure:"timeout"`
	CleanupPolicy  string        `mapstructure:"cleanup_policy"`
	ProbeCommand   []string      `mapstructure:"probe_command"`
}

// ValuesConfig controls immutable ValuesRevision validation.
type ValuesConfig struct {
	MaxDocumentBytes int64    `mapstructure:"max_document_bytes"`
	SecretPatterns   []string `mapstructure:"secret_patterns"`
}

// WithDefaults returns bounded ValuesRevision defaults.
func (c ValuesConfig) WithDefaults() ValuesConfig {
	if c.MaxDocumentBytes <= 0 {
		c.MaxDocumentBytes = 1 << 20
	}
	if c.SecretPatterns == nil {
		c.SecretPatterns = []string{}
	}
	return c
}

// Load reads the configuration from the given path.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	return &cfg, nil
}

// ArchiveCfg mirrors audit.ArchiveConfig for mapstructure unmarshalling.
type ArchiveCfg struct {
	RetentionDays     int           `mapstructure:"retention_days"`
	PollInterval      time.Duration `mapstructure:"poll_interval"`
	BatchSize         int           `mapstructure:"batch_size"`
	ArchiveDir        string        `mapstructure:"archive_dir"`
	Compression       string        `mapstructure:"compression"`
	ChecksumAlgorithm string        `mapstructure:"checksum_algorithm"`
}

// LoginRateLimitCfg controls the per-username login rate limiter (auth
// service only; other services ignore it). Zero values fall back to the
// auth service's defaults (5 attempts / 1 minute).
type LoginRateLimitCfg struct {
	MaxAttempts int           `mapstructure:"max_attempts"`
	Window      time.Duration `mapstructure:"window"`
}

// ServiceConfig holds flat configuration for individual microservices.
type ServiceConfig struct {
	HTTPPort       int               `mapstructure:"http_port"`
	LogLevel       string            `mapstructure:"log_level"`
	Audit          AuditCfg          `mapstructure:"audit"`
	Database       DatabaseConfig    `mapstructure:"database"`
	Redis          RedisConfig       `mapstructure:"redis"`
	Maintenance    bool              `mapstructure:"maintenance"`
	Authorization  AuthorizationCfg  `mapstructure:"authorization"`
	Values         ValuesConfig      `mapstructure:"values"`
	Gateway        GatewayCfg        `mapstructure:"gateway"`
	Agent          AgentCfg          `mapstructure:"agent"`
	CA             OperatorCACfg     `mapstructure:"ca"`
	LoginRateLimit LoginRateLimitCfg `mapstructure:"login_rate_limit"`
}

// AgentCfg controls the operator agent mode (TASK-075): the agent bootstraps
// its identity via enrollment token and connects to the gateway over mTLS.
// Mode "agent" is the default; "gateway" keeps the management-plane operator
// deployment (store + handler) that TASK-065 removes.
type AgentCfg struct {
	Mode                string `mapstructure:"mode"`
	CustomerID          string `mapstructure:"customer_id"`
	ClusterID           string `mapstructure:"cluster_id"`
	OperatorName        string `mapstructure:"operator_name"`
	EnrollmentTokenFile string `mapstructure:"enrollment_token_file"`
}

// WithDefaults returns the agent config with the default mode applied.
func (c AgentCfg) WithDefaults() AgentCfg {
	if c.Mode == "" {
		c.Mode = "agent"
	}
	return c
}

// OperatorCACfg points the operator agent at the gateway CA certificate used
// as the mTLS trust anchor.
type OperatorCACfg struct {
	CertPath string `mapstructure:"cert_path"`
}

// GatewayCfg controls the agent mTLS gateway listener (TASK-075): a second
// TLS listener that serves only the OperatorService handler for customer
// cluster agents. Enroll accepts certificate-less requests; CommandStream
// enforces client certificates (mixed mTLS contract, plan v1 Step 3).
type GatewayCfg struct {
	Enabled    bool   `mapstructure:"enabled"`
	Port       int    `mapstructure:"port"`
	CAKeyPath  string `mapstructure:"ca_key_path"`
	CACertPath string `mapstructure:"ca_cert_path"`
}

// WithDefaults returns bounded defaults for omitted gateway configuration.
// The gateway stays disabled unless explicitly enabled.
func (c GatewayCfg) WithDefaults() GatewayCfg {
	if c.Port == 0 {
		c.Port = 8084
	}
	return c
}

// AuthorizationCfg controls Authorization Snapshot polling and policy reload.
type AuthorizationCfg struct {
	AuthURL              string        `mapstructure:"auth_url"`
	PullInterval         time.Duration `mapstructure:"pull_interval"`
	PullBackoffMax       time.Duration `mapstructure:"pull_backoff_max"`
	PolicyReloadInterval time.Duration `mapstructure:"policy_reload_interval"`
}

// WithDefaults returns bounded REQ-027 defaults for omitted configuration.
func (c AuthorizationCfg) WithDefaults() AuthorizationCfg {
	if c.AuthURL == "" {
		c.AuthURL = "http://localhost:8085"
	}
	if c.PullInterval <= 0 {
		c.PullInterval = time.Second
	}
	if c.PullBackoffMax <= 0 {
		c.PullBackoffMax = 30 * time.Second
	}
	if c.PolicyReloadInterval <= 0 {
		c.PolicyReloadInterval = 5 * time.Second
	}
	return c
}

type AuditCfg struct {
	Archive ArchiveCfg `mapstructure:"archive"`
}

func bindDatabaseEnvironment(v *viper.Viper) error {
	bindings := map[string]string{
		"database.driver":                      "DATABASE_DRIVER",
		"database.dsn":                         "DATABASE_DSN",
		"database.max_open_conns":              "DATABASE_MAX_OPEN_CONNS",
		"database.max_idle_conns":              "DATABASE_MAX_IDLE_CONNS",
		"database.conn_max_lifetime":           "DATABASE_CONN_MAX_LIFETIME",
		"redis.address":                        "REDIS_ADDRESS",
		"redis.password":                       "REDIS_PASSWORD",
		"redis.db":                             "REDIS_DB",
		"maintenance":                          "MAINTENANCE",
		"authorization.auth_url":               "AUTHORIZATION_AUTH_URL",
		"authorization.pull_interval":          "AUTHORIZATION_PULL_INTERVAL",
		"authorization.pull_backoff_max":       "AUTHORIZATION_PULL_BACKOFF_MAX",
		"authorization.policy_reload_interval": "AUTHORIZATION_POLICY_RELOAD_INTERVAL",
		"gateway.enabled":                      "GATEWAY_ENABLED",
		"gateway.port":                         "GATEWAY_PORT",
		"gateway.ca_key_path":                  "GATEWAY_CA_KEY_PATH",
		"gateway.ca_cert_path":                 "GATEWAY_CA_CERT_PATH",
		"agent.customer_id":                    "CUSTOMER_ID",
		"agent.cluster_id":                     "CLUSTER_ID",
		"agent.operator_name":                  "OPERATOR_NAME",
		"agent.enrollment_token_file":          "ENROLLMENT_TOKEN_FILE",
		"ca.cert_path":                         "CA_CERT_PATH",
		"values.max_document_bytes":            "VALUES_MAX_DOCUMENT_BYTES",
		"values.secret_patterns":               "VALUES_SECRET_PATTERNS",
	}
	for key, envName := range bindings {
		if err := v.BindEnv(key, envName); err != nil {
			return fmt.Errorf("binding %s environment: %w", key, err)
		}
	}
	return nil
}

// LoadService reads a flat service configuration from the given path.
func LoadService(path string) (*ServiceConfig, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if err := bindDatabaseEnvironment(v); err != nil {
		return nil, err
	}

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg ServiceConfig
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	return &cfg, nil
}

// WatchConfigFile starts watching the config file for changes.
// onChange is called (with debounce) when the file is written.
// Returns a stop function that should be called on shutdown.
func WatchConfigFile(path string, onChange func()) (func(), error) {
	v := viper.New()
	v.SetConfigFile(path)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config for watch: %w", err)
	}

	v.WatchConfig()

	// Debounce: wait for a quiet period before firing.
	var debounceTimer *time.Timer
	const debounceInterval = 500 * time.Millisecond

	v.OnConfigChange(func(_ fsnotify.Event) {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.AfterFunc(debounceInterval, onChange)
	})

	return func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
	}, nil
}
