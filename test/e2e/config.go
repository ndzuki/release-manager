package e2e

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrConfigInvalid marks a configuration that cannot be used to start E2E.
var ErrConfigInvalid = errors.New("config invalid")

// ErrE2ERunIDInvalid marks an E2E_RUN_ID that is not a DNS-1123 label.
var ErrE2ERunIDInvalid = errors.New("e2e_run_id_invalid")

var (
	dns1123LabelPattern        = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$`)
	environmentVariablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ConfigError is returned when an env-config value is invalid. Its Error
// method intentionally contains only field names and safe validation details.
type ConfigError struct {
	Field  string
	Reason string
	cause  error
}

func (e *ConfigError) Error() string {
	if e == nil {
		return ErrConfigInvalid.Error()
	}
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", ErrConfigInvalid, e.Reason)
	}
	if e.Reason == "" {
		return fmt.Sprintf("%s: %s", ErrConfigInvalid, e.Field)
	}
	return fmt.Sprintf("%s: %s: %s", ErrConfigInvalid, e.Field, e.Reason)
}

func (e *ConfigError) Unwrap() error {
	if e != nil && e.cause != nil {
		return e.cause
	}
	return ErrConfigInvalid
}

// Endpoints contains the HTTP/Connect endpoints used by the runner. The
// orchestrator endpoint is the only endpoint required by the startup contract;
// the remaining endpoints are validated when supplied and are required by the
// stages that consume them.
type Endpoints struct {
	ReleaseOrchestrator string `yaml:"release_orchestrator" json:"release_orchestrator"`
	ReleaseWebhook      string `yaml:"release_webhook" json:"release_webhook,omitempty"`
	ReleaseOperator     string `yaml:"release_operator" json:"release_operator,omitempty"`
	ReleaseAuth         string `yaml:"release_auth" json:"release_auth,omitempty"`
	ReleaseNotifier     string `yaml:"release_notifier" json:"release_notifier,omitempty"`
	ReleaseAPI          string `yaml:"release_api" json:"release_api,omitempty"`
}

// E2ERunnerCredentials contains a username and an environment variable name.
// The password itself is deliberately not represented in this serializable
// type; Config keeps the resolved value private for the process lifetime.
type E2ERunnerCredentials struct {
	Username    string `yaml:"username" json:"username"`
	PasswordEnv string `yaml:"password_env" json:"password_env"`
}

// Credentials contains credentials consumed by E2E. No operator credential
// block is accepted by the strict YAML decoder.
type Credentials struct {
	E2ERunner E2ERunnerCredentials `yaml:"e2e_runner" json:"e2e_runner"`
}

// RestartTargets is the static, least-privilege restart binding. The list is
// expected to contain the Auth, operator gateway, and Orchestrator Deployments.
type RestartTargets struct {
	Namespace   string   `yaml:"namespace" json:"namespace"`
	Deployments []string `yaml:"deployments" json:"deployments"`
}

// K3dConfig contains the Kubernetes inputs used by E2E.
type K3dConfig struct {
	Kubeconfig     string         `yaml:"kubeconfig" json:"kubeconfig"`
	TestNamespace  string         `yaml:"test_namespace" json:"test_namespace"`
	RestartTargets RestartTargets `yaml:"restart_targets" json:"restart_targets"`
}

// ExpectedIdentity is the manifest-derived identity contract consumed by the
// inventory and fixture guards. Counts are intentionally data-driven rather
// than hard-coded in the runner.
type ExpectedIdentity struct {
	Customers        int      `yaml:"customers" json:"customers"`
	Clusters         int      `yaml:"clusters" json:"clusters"`
	RoutesBasic      int      `yaml:"routes_basic" json:"routes_basic"`
	DefinitionsBasic int      `yaml:"definitions_basic" json:"definitions_basic"`
	Bundles          int      `yaml:"bundles" json:"bundles"`
	E2EDefinitionIDs []string `yaml:"e2e_definition_ids" json:"e2e_definition_ids"`
}

// E2EUpgradeTarget contains the manifest-derived identifiers needed to submit
// an UPGRADE. The current revision is intentionally absent and must be read
// from inventory at runtime.
type E2EUpgradeTarget struct {
	DefinitionID     string `yaml:"definition_id" json:"definition_id"`
	BundleID         string `yaml:"bundle_id" json:"bundle_id"`
	ValuesRevisionID string `yaml:"values_revision_id" json:"values_revision_id"`
}

// SeedConfig contains the seed fixture identity and upgrade inputs.
type SeedConfig struct {
	Customers           []string           `yaml:"customers" json:"customers"`
	ClustersPerCustomer int                `yaml:"clusters_per_customer" json:"clusters_per_customer"`
	FixtureVersion      string             `yaml:"fixture_version" json:"fixture_version"`
	ExpectedIdentity    ExpectedIdentity   `yaml:"expected_identity" json:"expected_identity"`
	E2EUpgradeTargets   []E2EUpgradeTarget `yaml:"e2e_upgrade_targets" json:"e2e_upgrade_targets"`
}

// Config is the runtime env-config consumed by cmd/e2e. Its resolved password
// is private and therefore cannot be emitted by YAML or JSON serialization.
type Config struct {
	Environment   string      `yaml:"environment" json:"environment"`
	EnvironmentID string      `yaml:"environment_id" json:"environment_id"`
	Endpoints     Endpoints   `yaml:"endpoints" json:"endpoints"`
	Credentials   Credentials `yaml:"credentials" json:"credentials"`
	K3d           K3dConfig   `yaml:"k3d" json:"k3d"`
	Seed          SeedConfig  `yaml:"seed" json:"seed"`

	password string
}

// LoadConfig reads, strictly decodes, validates, and resolves the configured
// e2e-runner password from its named process environment variable. Secret
// bytes are never read from YAML and never serialized into Config.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, configInvalid("config", "read config file")
	}

	var cfg Config
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, configInvalid("config", "decode yaml")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, configInvalid("config", "multiple yaml documents")
		}
		return nil, configInvalid("config", "decode yaml")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ParseConfig decodes and validates YAML bytes. It is useful for tests and
// callers that already own the configuration bytes.
func ParseConfig(data []byte) (*Config, error) {
	var cfg Config
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, configInvalid("config", "decode yaml")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, configInvalid("config", "multiple yaml documents")
		}
		return nil, configInvalid("config", "decode yaml")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks the static schema and resolves the password named by
// credentials.e2e_runner.password_env from the current process environment.
func (c *Config) Validate() error {
	if c == nil {
		return configInvalid("config", "nil config")
	}
	if runID, ok := os.LookupEnv("E2E_RUN_ID"); ok {
		if err := ValidateRunID(runID); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.Endpoints.ReleaseOrchestrator) == "" {
		return configInvalid("endpoints.release_orchestrator", "missing")
	}
	for _, endpoint := range []struct {
		field string
		value string
	}{
		{field: "endpoints.release_orchestrator", value: c.Endpoints.ReleaseOrchestrator},
		{field: "endpoints.release_webhook", value: c.Endpoints.ReleaseWebhook},
		{field: "endpoints.release_operator", value: c.Endpoints.ReleaseOperator},
		{field: "endpoints.release_auth", value: c.Endpoints.ReleaseAuth},
		{field: "endpoints.release_notifier", value: c.Endpoints.ReleaseNotifier},
		{field: "endpoints.release_api", value: c.Endpoints.ReleaseAPI},
	} {
		if endpoint.value == "" {
			continue
		}
		if err := validateEndpoint(endpoint.value); err != nil {
			return configInvalid(endpoint.field, "must be an absolute http or https URL")
		}
	}
	if strings.TrimSpace(c.Environment) == "" {
		return configInvalid("environment", "missing")
	}
	if strings.TrimSpace(c.EnvironmentID) == "" {
		return configInvalid("environment_id", "missing")
	}
	if strings.TrimSpace(c.Credentials.E2ERunner.Username) == "" {
		return configInvalid("credentials.e2e_runner.username", "missing")
	}
	passwordEnv := strings.TrimSpace(c.Credentials.E2ERunner.PasswordEnv)
	if passwordEnv == "" {
		return configInvalid("credentials.e2e_runner.password_env", "missing")
	}
	if !environmentVariablePattern.MatchString(passwordEnv) {
		return configInvalid("credentials.e2e_runner.password_env", "invalid environment variable name")
	}
	password, ok := os.LookupEnv(passwordEnv)
	if !ok || password == "" {
		return configInvalid("credentials.e2e_runner.password_env", "environment variable is not set")
	}
	c.password = password

	if strings.TrimSpace(c.K3d.Kubeconfig) == "" {
		return configInvalid("k3d.kubeconfig", "missing")
	}
	if err := validateDNS1123Label(c.K3d.TestNamespace); err != nil {
		return configInvalid("k3d.test_namespace", "must be a DNS-1123 label")
	}
	if strings.TrimSpace(c.K3d.RestartTargets.Namespace) == "" {
		return configInvalid("k3d.restart_targets.namespace", "missing")
	}
	if c.K3d.RestartTargets.Namespace != c.K3d.TestNamespace {
		return configInvalid("k3d.restart_targets.namespace", "must equal k3d.test_namespace")
	}
	if len(c.K3d.RestartTargets.Deployments) != 3 {
		return configInvalid("k3d.restart_targets.deployments", "must contain exactly three deployment names")
	}
	seenDeployments := make(map[string]struct{}, len(c.K3d.RestartTargets.Deployments))
	for _, deployment := range c.K3d.RestartTargets.Deployments {
		if err := validateDNS1123Label(deployment); err != nil {
			return configInvalid("k3d.restart_targets.deployments", "contains an invalid deployment name")
		}
		if _, ok := seenDeployments[deployment]; ok {
			return configInvalid("k3d.restart_targets.deployments", "contains duplicate deployment names")
		}
		seenDeployments[deployment] = struct{}{}
	}

	if len(c.Seed.Customers) == 0 {
		return configInvalid("seed.customers", "missing")
	}
	if c.Seed.ClustersPerCustomer < 1 {
		return configInvalid("seed.clusters_per_customer", "must be positive")
	}
	if strings.TrimSpace(c.Seed.FixtureVersion) == "" {
		return configInvalid("seed.fixture_version", "missing")
	}
	if err := validateExpectedIdentity(c.Seed.ExpectedIdentity); err != nil {
		return err
	}
	if err := validateUpgradeTargets(c.Seed.E2EUpgradeTargets); err != nil {
		return err
	}
	return nil
}

// Password returns the process-local e2e-runner password resolved by Validate.
// It is intentionally absent from Config's serialized representation.
func (c *Config) Password() string {
	if c == nil {
		return ""
	}
	return c.password
}

// RunIDFromEnv validates E2E_RUN_ID when present. An unset variable is valid
// for local runs, where cmd/e2e generates a local identifier.
func RunIDFromEnv() (string, error) {
	runID, ok := os.LookupEnv("E2E_RUN_ID")
	if !ok {
		return "", nil
	}
	if err := ValidateRunID(runID); err != nil {
		return "", err
	}
	return runID, nil
}

// ValidateRunID validates the DNS-1123 label used to correlate CI resources.
func ValidateRunID(runID string) error {
	if !dns1123LabelPattern.MatchString(runID) {
		return fmt.Errorf("%w: must be a DNS-1123 label", ErrE2ERunIDInvalid)
	}
	return nil
}

// ValidateE2ERunID is an explicit alias for callers that prefer the domain
// name in the function identifier.
func ValidateE2ERunID(runID string) error { return ValidateRunID(runID) }

func configInvalid(field, reason string) error {
	return &ConfigError{Field: field, Reason: reason}
}

func validateEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("invalid endpoint")
	}
	return nil
}

func validateDNS1123Label(value string) error {
	if !dns1123LabelPattern.MatchString(value) {
		return errors.New("invalid dns-1123 label")
	}
	return nil
}

func validateExpectedIdentity(identity ExpectedIdentity) error {
	for field, value := range map[string]int{
		"seed.expected_identity.customers":         identity.Customers,
		"seed.expected_identity.clusters":          identity.Clusters,
		"seed.expected_identity.routes_basic":      identity.RoutesBasic,
		"seed.expected_identity.definitions_basic": identity.DefinitionsBasic,
		"seed.expected_identity.bundles":           identity.Bundles,
	} {
		if value < 0 {
			return configInvalid(field, "must not be negative")
		}
	}
	if len(identity.E2EDefinitionIDs) == 0 {
		return configInvalid("seed.expected_identity.e2e_definition_ids", "missing")
	}
	seen := make(map[string]struct{}, len(identity.E2EDefinitionIDs))
	for _, definitionID := range identity.E2EDefinitionIDs {
		if strings.TrimSpace(definitionID) == "" {
			return configInvalid("seed.expected_identity.e2e_definition_ids", "contains an empty definition id")
		}
		if _, ok := seen[definitionID]; ok {
			return configInvalid("seed.expected_identity.e2e_definition_ids", "contains duplicate definition ids")
		}
		seen[definitionID] = struct{}{}
	}
	return nil
}

func validateUpgradeTargets(targets []E2EUpgradeTarget) error {
	if len(targets) == 0 {
		return configInvalid("seed.e2e_upgrade_targets", "missing")
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target.DefinitionID == "" {
			return configInvalid("seed.e2e_upgrade_targets.definition_id", "missing")
		}
		if target.BundleID == "" {
			return configInvalid("seed.e2e_upgrade_targets.bundle_id", "missing")
		}
		if target.ValuesRevisionID == "" {
			return configInvalid("seed.e2e_upgrade_targets.values_revision_id", "missing")
		}
		if _, ok := seen[target.DefinitionID]; ok {
			return configInvalid("seed.e2e_upgrade_targets.definition_id", "contains duplicate definition ids")
		}
		seen[target.DefinitionID] = struct{}{}
	}
	for _, required := range []string{
		"e2e-release-target",
		"e2e-isolation-target",
		"e2e-restart-target",
	} {
		if _, ok := seen[required]; !ok {
			return configInvalid("seed.e2e_upgrade_targets", "missing "+required)
		}
	}
	return nil
}
