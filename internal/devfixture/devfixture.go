// Package devfixture seeds and resets the deterministic development fixture
// (REQ-065) through the formal public Connect service seams. It never touches
// the database directly during seeding — every entity is created via the
// orchestrator, auth, webhook, and trust services — so the runner works
// against any deployment that exposes those APIs.
//
// The seed is organized as nine committed phases (identity, routing,
// accounts, trust, bundle, values, enrollment, install, verify). Progress is
// persisted atomically to data/dev-seed-progress.json; a re-run verifies
// canonical consistency (entity existence + stable logical keys, never
// server-generated mutable fields) of already-committed phases and resumes
// from the first non-committed phase. When every phase is committed the run
// reports "fixture up-to-date" and issues no write RPCs (AC-065-03).
//
// Reset (dev-reset-data) rebuilds the databases SDK-only: it rolls every
// migration down to the baseline and back up to the latest schema, then
// re-seeds all nine phases. On failure the progress file is marked
// "partial": true (the shell layer owns pg_restore recovery).
package devfixture

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Mode selects the credential/secret handling profile (REQ-065).
type Mode string

const (
	// ModeLocal writes credentials and the dev trust root key to disk.
	ModeLocal Mode = "local"
	// ModeCI injects credentials and the trust root key from the environment
	// and never writes secret material to disk.
	ModeCI Mode = "ci"
)

// Defaults for seed runtime knobs.
const (
	DefaultFixtureVersion = "v2"
	DefaultDataDir        = "data"
	DefaultChartDir       = "deploy/fixtures/chart"
	DefaultTrustEnv       = "staging" // orchestrator --target-env in the dev kustomize
	DefaultInstallTimeout = 15 * time.Minute
	DefaultInstallPoll    = 3 * time.Second
)

// ErrFixtureConflict reports canonical drift of an already-committed phase:
// an entity is missing or its stable logical key no longer matches the seed
// contract.
var ErrFixtureConflict = errors.New("fixture_conflict")

// Config carries every knob for the seed runner. Zero values fall back to
// defaults; secrets left empty are resolved from data/dev-credentials.env
// (local mode, reuse-or-generate) or fail in CI mode.
type Config struct {
	Mode Mode

	OrchestratorURL string
	WebhookURL      string
	AuthURL         string

	DataDir string

	AdminUser        string
	AdminPassword    string // optional override; falls back to credentials file
	DeployerUser     string
	DeployerPassword string // optional override; falls back to credentials file
	ReaderPassword   string // optional override; generated in local mode

	TrustEnvironment    string
	TrustRootPrivateKey string // CI mode: PEM-encoded Ed25519 private key

	FixtureVersion string
	ChartDir       string

	InstallTimeout    time.Duration
	InstallPollPeriod time.Duration

	Logger *slog.Logger
	Now    func() time.Time // test hook; defaults to time.Now

	// DatabaseDSN opens the PostgreSQL schema for Reset. ReleaseNotifierDSN
	// is optional and skipped when empty.
	DatabaseDSN        string
	ReleaseNotifierDSN string

	// MigrationsFactory opens the schema-migration seam used by Reset.
	// Tests inject a fake; production uses NewPostgresMigrations.
	MigrationsFactory func(dsn string) (Migrations, error)
}

// CustomerRef is the manifest entry for one seeded customer.
type CustomerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ClusterRef is the manifest entry for one seeded cluster.
type ClusterRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DefinitionRef is the manifest entry for one seeded release definition.
type DefinitionRef struct {
	ID               string `json:"id"`
	ValuesRevisionID string `json:"values_revision_id"`
	BundleID         string `json:"bundle_id"`
}

// BundleRef is the manifest entry for the seeded release bundle.
type BundleRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// Manifest is the runtime identity manifest written to
// data/dev-fixture.json (REQ-065): stable logical keys mapping to the
// server-generated identifiers that later stages (REQ-066) reference.
type Manifest struct {
	FixtureVersion string                   `json:"fixture_version"`
	GeneratedAt    string                   `json:"generated_at"`
	Customers      map[string]CustomerRef   `json:"customers"`
	Clusters       map[string]ClusterRef    `json:"clusters"`
	Definitions    map[string]DefinitionRef `json:"definitions"`
	Bundle         BundleRef                `json:"bundle"`
}

// Run seeds the full fixture idempotently and returns the runtime manifest.
// When every phase is already committed, Run issues no write RPC, logs
// "fixture up-to-date" (AC-065-03), and returns the existing manifest.
func Run(ctx context.Context, cfg Config) (*Manifest, error) {
	cfg = cfg.withDefaults()
	runner, err := newRunner(cfg)
	if err != nil {
		return nil, err
	}
	return runner.run(ctx)
}

// Reset rebuilds the databases and re-seeds every phase, ignoring the
// fixture version check (force rebuild). On failure the progress file is
// marked "partial": true with its phases structure preserved; the caller's
// shell layer owns pg_restore recovery.
func Reset(ctx context.Context, cfg Config) (*Manifest, error) {
	cfg = cfg.withDefaults()
	runner, err := newRunner(cfg)
	if err != nil {
		return nil, err
	}
	return runner.reset(ctx)
}

func (c Config) withDefaults() Config {
	if c.Mode == "" {
		c.Mode = ModeLocal
	}
	if c.OrchestratorURL == "" {
		c.OrchestratorURL = "http://localhost:8083"
	}
	if c.WebhookURL == "" {
		c.WebhookURL = "http://localhost:8082"
	}
	if c.AuthURL == "" {
		c.AuthURL = "http://localhost:8085"
	}
	if c.DataDir == "" {
		c.DataDir = DefaultDataDir
	}
	if c.AdminUser == "" {
		c.AdminUser = "admin"
	}
	if c.DeployerUser == "" {
		c.DeployerUser = "deployer"
	}
	if c.TrustEnvironment == "" {
		c.TrustEnvironment = DefaultTrustEnv
	}
	if c.FixtureVersion == "" {
		c.FixtureVersion = DefaultFixtureVersion
	}
	if c.ChartDir == "" {
		c.ChartDir = DefaultChartDir
	}
	if c.InstallTimeout <= 0 {
		c.InstallTimeout = DefaultInstallTimeout
	}
	if c.InstallPollPeriod <= 0 {
		c.InstallPollPeriod = DefaultInstallPoll
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.MigrationsFactory == nil {
		c.MigrationsFactory = NewPostgresMigrations
	}
	return c
}

func (c Config) now() time.Time { return c.Now().UTC() }

func (c Config) nowRFC3339() string { return c.Now().UTC().Format(time.RFC3339) }

func (c Config) log() *slog.Logger { return c.Logger }

func wrapConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrFixtureConflict, fmt.Sprintf(format, args...))
}
