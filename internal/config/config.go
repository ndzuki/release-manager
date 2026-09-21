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
	CA             CAConfig          `mapstructure:"ca"`
	LoginRateLimit LoginRateLimitCfg `mapstructure:"login_rate_limit"`
	// OperatorSession carries the REQ-044 liveness thresholds (TASK-098): the
	// agent heartbeat cadence the orchestrator negotiates, and how long a
	// session may miss heartbeats before it becomes suspect / offline.
	OperatorSession OperatorSessionCfg `mapstructure:"operator_session"`
	// Operation carries the REQ-023 standard-operation deadline and the
	// recovery sweep cadence (TASK-098). A zero value falls back to
	// WithDefaults.
	Operation OperationCfg `mapstructure:"operation"`
	// Notifier carries the REQ-031 notifier policy (TASK-096): the outbound
	// webhook egress allowlist and the ADR-020 secret resolver settings.
	Notifier NotifierCfg `mapstructure:"notifier"`
	// VulnerabilityAdmission carries the artifact admission mode (TASK-105).
	VulnerabilityAdmission VulnerabilityAdmissionCfg `mapstructure:"vulnerability_admission"`
	// RuntimePullPreflight carries the REQ-048 runtime-pull preflight policy
	// (TASK-114): the operator builds its preflight runtime_pull stage executor
	// from it. Disabled by default — the stage is optional in the production
	// pipeline, so a disabled executor fails that stage without blocking the
	// release.
	RuntimePullPreflight RuntimePullConfig `mapstructure:"runtime_pull_preflight"`
}

// VulnerabilityAdmissionCfg is how artifact admission treats the vulnerability
// evaluator's answer (TASK-105).
type VulnerabilityAdmissionCfg struct {
	// Mode is one of off, shadow, enforce.
	//
	// shadow is the default on purpose: the admission step used to be unwired, so
	// switching straight to enforce would start blocking releases in deployments
	// whose scanner is not configured yet. shadow keeps the outcome identical and
	// records what would have been blocked, which is the evidence an operator needs
	// before opting into enforce. off skips the evaluation entirely.
	Mode string `mapstructure:"mode"`
}

// WithDefaults fills the mode when the section is absent or empty.
func (c VulnerabilityAdmissionCfg) WithDefaults() VulnerabilityAdmissionCfg {
	if strings.TrimSpace(c.Mode) == "" {
		c.Mode = "shadow"
	}
	return c
}

// Validate rejects an unknown mode instead of silently falling back to a default:
// a typo in this value decides whether releases are blocked.
func (c VulnerabilityAdmissionCfg) Validate() error {
	switch c.WithDefaults().Mode {
	case "off", "shadow", "enforce":
		return nil
	default:
		return fmt.Errorf("vulnerability_admission.mode must be off, shadow or enforce, got %q", c.Mode)
	}
}

// NotifierCfg is the REQ-031 notifier surface policy (TASK-096).
type NotifierCfg struct {
	// URL is the notifier service the orchestrator's terminal-notification
	// outbox worker calls over Connect (REQ-031 AC-031-12). Empty leaves the
	// worker disabled: entries stay queued rather than being acknowledged
	// without being sent.
	URL string `mapstructure:"url"`
	// Recipient is the webhook URL carried into SendNotificationRequest.
	// REQ-031's payload contract names channel and recipient but not where they
	// come from, and no per-organization notification settings exist yet, so
	// D-N (2026-09-19) chose a single deployment-wide default for now.
	Recipient string `mapstructure:"recipient"`
	// DeliveryChannel is the channel name the worker sends on; webhook is the
	// only one the notifier implements today.
	DeliveryChannel string `mapstructure:"delivery_channel"`
	// PollInterval is how often the worker drains the outbox.
	PollInterval time.Duration `mapstructure:"poll_interval"`
	// EgressAllowlist is the complete set of destinations outbound webhook
	// delivery may reach, as "scheme://host:port" entries (the port may be
	// omitted for the scheme default). It is deny-by-default: an empty list
	// blocks every outbound destination, so enabling delivery is an explicit
	// configuration act. Widening it is a configuration change, never a
	// request-side parameter.
	EgressAllowlist []string `mapstructure:"egress_allowlist"`
	// Vault is the ADR-020 Kubernetes-auth KV v2 resolver. Disabled by default;
	// when enabled, missing or invalid settings fail startup (fail closed)
	// rather than silently delivering without a credential.
	Vault VaultResolverCfg `mapstructure:"vault"`
}

// WithDeliveryDefaults returns bounded REQ-031 delivery defaults for omitted
// configuration.
func (c NotifierCfg) WithDeliveryDefaults() NotifierCfg {
	if c.DeliveryChannel == "" {
		c.DeliveryChannel = "webhook"
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 30 * time.Second
	}
	return c
}

// DeliveryEnabled reports whether the terminal-notification worker can run: it
// needs both an address to call and a recipient to send to.
func (c NotifierCfg) DeliveryEnabled() bool {
	return c.URL != "" && c.Recipient != ""
}

// VaultResolverCfg carries the ADR-020 resolver references. Every field is a
// reference or a path: no secret value belongs in configuration.
type VaultResolverCfg struct {
	Enabled    bool   `mapstructure:"enabled"`
	Address    string `mapstructure:"address"`
	Namespace  string `mapstructure:"namespace"`
	AuthMount  string `mapstructure:"auth_mount"`
	Role       string `mapstructure:"role"`
	TokenPath  string `mapstructure:"token_path"`
	KVMount    string `mapstructure:"kv_mount"`
	SecretPath string `mapstructure:"secret_path"`
	SecretKey  string `mapstructure:"secret_key"`
}

// WithDefaults fills the ADR-020 resolver defaults: the Kubernetes auth mount,
// the projected service-account token path and the KV v2 mount. Address, role,
// path and key stay empty on purpose — they name a deployment's secret, so the
// operator must supply them, and Validate reports what is missing.
func (c VaultResolverCfg) WithDefaults() VaultResolverCfg {
	if c.AuthMount == "" {
		c.AuthMount = "kubernetes"
	}
	if c.TokenPath == "" {
		c.TokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	}
	if c.KVMount == "" {
		c.KVMount = "secret"
	}
	return c
}

// Validate reports the first missing reference when the resolver is enabled.
// A disabled resolver is always valid: ADR-020 keeps unauthenticated delivery
// when no resolver is configured (REQ-031), and that branch is explicit.
func (c VaultResolverCfg) Validate() error {
	if !c.Enabled {
		return nil
	}
	missing := []struct {
		name  string
		value string
	}{
		{"address", c.Address},
		{"role", c.Role},
		{"secret_path", c.SecretPath},
		{"secret_key", c.SecretKey},
	}
	for _, field := range missing {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("notifier.vault.%s is required when the resolver is enabled", field.name)
		}
	}
	return nil
}

// OperatorSessionCfg are the operator liveness thresholds (REQ-044).
type OperatorSessionCfg struct {
	// HeartbeatInterval is how often the agent must send a heartbeat; the
	// orchestrator negotiates it in SessionEstablished.
	HeartbeatInterval time.Duration `mapstructure:"heartbeat_interval"`
	// SuspectAfter marks a session whose heartbeats stopped.
	SuspectAfter time.Duration `mapstructure:"suspect_after"`
	// OfflineAfter marks a session offline; the emergency path treats this as
	// "operator offline" (REQ-032 operator_offline).
	OfflineAfter time.Duration `mapstructure:"offline_after"`
}

// WithDefaults returns bounded REQ-044 defaults. 15s/45s/90s tolerate two lost
// heartbeats before suspect and four before offline, so one dropped frame or a
// brief network blip cannot remove a healthy cluster from the emergency path,
// while a dead agent still reaches `operator_offline` inside 90 seconds.
func (c OperatorSessionCfg) WithDefaults() OperatorSessionCfg {
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 15 * time.Second
	}
	if c.SuspectAfter <= 0 {
		c.SuspectAfter = 45 * time.Second
	}
	if c.OfflineAfter <= 0 {
		c.OfflineAfter = 90 * time.Second
	}
	return c
}

// OperationCfg carries the standard-operation lifecycle bounds (REQ-023).
type OperationCfg struct {
	// Deadline bounds an INSTALL/UPGRADE/ROLLBACK operation end to end; a
	// non-terminal operation past it (plus the recovery grace period) is
	// transitioned to timeout by the recovery sweep.
	Deadline time.Duration `mapstructure:"deadline"`
	// RecoveryInterval is the period of the non-terminal recovery sweep.
	RecoveryInterval time.Duration `mapstructure:"recovery_interval"`
}

// WithDefaults returns the REQ-023 defaults: a 30-minute operation deadline and
// a 1-minute recovery sweep. The deadline matches the E2E install budget; the
// sweep is frequent enough that a stuck operation is collected within a minute
// of becoming stale (and it is idempotent, so a tighter period is safe).
func (c OperationCfg) WithDefaults() OperationCfg {
	if c.Deadline <= 0 {
		c.Deadline = 30 * time.Minute
	}
	if c.RecoveryInterval <= 0 {
		c.RecoveryInterval = time.Minute
	}
	return c
}

// EmergencyCfg carries the emergency change kill switch and operation
// timeout (REQ-081 D2=A) plus the stuck-lock observation window (REQ-087
// D5=B): the orchestrator loads them at startup (see cmd/orchestrator
// loadEmergencyConfig) and seeds them into the shared app_settings table as
// the production writer of SetEmergencyConfig. A missing section fails
// closed (Enabled=false, default timeout / observe window). Durations are
// decoded from their raw string form so an unparsable value falls back to
// the defaults instead of failing config loading.
type EmergencyCfg struct {
	Enabled              bool   `mapstructure:"enabled"`
	OperationTimeout     string `mapstructure:"operation_timeout"`
	EffectObserveTimeout string `mapstructure:"effect_observe_timeout"`
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
	// RegistryPlainHTTP allows the operator to pull OCI charts from a plain
	// HTTP registry (dev fixture only — the local registry is loopback-bound
	// and unauthenticated, AC-065-01 D8). Production registries are HTTPS and
	// must NOT set this (defaults false).
	RegistryPlainHTTP bool `mapstructure:"registry_plain_http"`
}

// WithDefaults returns the agent config with the default mode applied.
func (c AgentCfg) WithDefaults() AgentCfg {
	if c.Mode == "" {
		c.Mode = "agent"
	}
	return c
}

// CAConfig controls the persistent operator certificate authority (REQ-015,
// ADR-017): production loads credentials from Vault KV, dev persists them as
// files. The same CA cert doubles as the agent-side mTLS trust anchor.
type CAConfig struct {
	VaultPath        string        `mapstructure:"vault_path"`
	KeyPath          string        `mapstructure:"key_path"`
	CertPath         string        `mapstructure:"cert_path"`
	CertTTL          time.Duration `mapstructure:"cert_ttl"`
	RenewBeforeRatio float64       `mapstructure:"renew_before_ratio"`
}

// WithDefaults returns the CA config with bounded defaults applied.
func (c CAConfig) WithDefaults() CAConfig {
	if c.CertTTL <= 0 {
		c.CertTTL = 7 * 24 * time.Hour
	}
	if c.RenewBeforeRatio == 0 {
		c.RenewBeforeRatio = 0.5
	}
	return c
}

// Validate checks the CA credential source without exposing secrets.
// VaultPath or the dev file pair must be configured; Vault unavailable is a
// fail-closed startup error (Step 4 验收).
func (c CAConfig) Validate() error {
	c = c.WithDefaults()
	if c.RenewBeforeRatio <= 0 || c.RenewBeforeRatio > 1 {
		return fmt.Errorf("ca_invalid: ca.renew_before_ratio must be within (0, 1]")
	}
	if c.VaultPath != "" {
		return nil
	}
	if strings.TrimSpace(c.KeyPath) == "" || strings.TrimSpace(c.CertPath) == "" {
		return fmt.Errorf("ca_invalid: ca.vault_path or ca.key_path and ca.cert_path are required")
	}
	return nil
}

// GatewayCfg controls the agent mTLS gateway listener (TASK-075): a second
// TLS listener that serves only the OperatorService handler for customer
// cluster agents. Enroll accepts certificate-less requests; CommandStream
// enforces client certificates (mixed mTLS contract, plan v1 Step 3).
// The CA trust anchor lives in the top-level CAConfig (ADR-017); the legacy
// ca_key_path/ca_cert_path fields were dead config with zero readers and are
// removed (TASK-094 §7-4).
type GatewayCfg struct {
	Enabled bool `mapstructure:"enabled"`
	Port    int  `mapstructure:"port"`
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
		"notifier.url":                         "NOTIFIER_URL",
		"notifier.recipient":                   "NOTIFIER_RECIPIENT",
		"notifier.delivery_channel":            "NOTIFIER_DELIVERY_CHANNEL",
		"notifier.poll_interval":               "NOTIFIER_POLL_INTERVAL",
		"authorization.pull_interval":          "AUTHORIZATION_PULL_INTERVAL",
		"authorization.pull_backoff_max":       "AUTHORIZATION_PULL_BACKOFF_MAX",
		"authorization.policy_reload_interval": "AUTHORIZATION_POLICY_RELOAD_INTERVAL",
		"gateway.enabled":                      "GATEWAY_ENABLED",
		"gateway.port":                         "GATEWAY_PORT",
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
