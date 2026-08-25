// Command devseed seeds (or resets) the deterministic development fixture
// (REQ-065) through the formal public service seams. It is a thin CLI over
// internal/devfixture: flags and environment variables configure the runner,
// and the output contract (development seed complete / namespace / endpoints)
// is preserved for the dev.sh lifecycle module.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ndzuki/release-manager/internal/devfixture"
)

const (
	defaultOrchestratorURL = "http://localhost:8083"
	defaultWebhookURL      = "http://localhost:8082"
	defaultAuthURL         = "http://localhost:8085"
	developmentNamespace   = "release-manager-dev"
)

func main() {
	cfg := devfixture.Config{}
	var (
		printFixtureVersion bool
		operatorTimeoutSecs int
		seedRetries         int
	)
	flag.BoolVar(&printFixtureVersion, "print-fixture-version", false, "print the authoritative fixture version constant and exit (AC-065-30)")
	flag.IntVar(&operatorTimeoutSecs, "operator-timeout", 0, "operator-online wait timeout in seconds (env DEV_TIMEOUT_OPERATOR, default 180; AC-065-28)")
	flag.IntVar(&seedRetries, "seed-retries", 0, "seed phase-write retry count with 1s/2s/4s backoff (env DEV_TIMEOUT_SEED_RETRIES, default 3; AC-065-28)")
	flag.StringVar(&cfg.StopAfterPhase, "stop-after", "", "commit phases up to and including this phase, then exit cleanly (e.g. enrollment; dev.sh resumes with a later run)")
	flag.BoolVar(&resetMode, "reset", false, "rebuild databases and re-seed (dev-reset-data)")
	flag.StringVar(&cfg.OrchestratorURL, "orchestrator", defaultOrchestratorURL, "Orchestrator Connect URL")
	flag.StringVar(&cfg.WebhookURL, "webhook", defaultWebhookURL, "Webhook Connect URL")
	flag.StringVar(&cfg.AuthURL, "auth", defaultAuthURL, "Auth Connect URL")
	flag.StringVar(&cfg.AdminUser, "admin-user", envOr("DEV_ADMIN_USER", "dev-admin"), "platform admin username (env DEV_ADMIN_USER; REQ-065 contract name dev-admin)")
	flag.StringVar(&cfg.AdminPassword, "admin-password", os.Getenv("DEV_ADMIN_PASSWORD"), "platform admin password (env DEV_ADMIN_PASSWORD; falls back to data/dev-credentials.env)")
	flag.StringVar(&cfg.DeployerUser, "deployer-user", envOr("DEV_DEPLOYER_USER", "dev-deployer"), "deployer username (env DEV_DEPLOYER_USER; REQ-065 contract name dev-deployer)")
	flag.StringVar(&cfg.DeployerPassword, "deployer-password", os.Getenv("DEV_DEPLOYER_PASSWORD"), "deployer password (env DEV_DEPLOYER_PASSWORD; falls back to data/dev-credentials.env)")
	flag.StringVar(&cfg.ReaderPassword, "reader-password", os.Getenv("DEV_READER_PASSWORD"), "reader password (env DEV_READER_PASSWORD; generated in local mode)")
	flag.StringVar(&cfg.E2ERunnerPassword, "e2e-runner-password", os.Getenv("E2E_RUNNER_PASSWORD"), "e2e-runner password (env E2E_RUNNER_PASSWORD; release_admin role, generated in local mode)")
	flag.StringVar(&cfg.TrustRootPrivateKey, "trust-root-private-key", os.Getenv("DEV_TRUST_ROOT_PRIVATE_KEY"), "Dev Trust Root Ed25519 private key, PEM or base64 PEM (env DEV_TRUST_ROOT_PRIVATE_KEY; required in ci profile)")
	flag.StringVar(&cfg.DataDir, "data-dir", devfixture.DefaultDataDir, "directory for dev-fixture.json, progress, credentials and tokens")
	flag.StringVar(&cfg.DatabaseDSN, "database-dsn", os.Getenv("RELEASE_MANAGER_DATABASE_DSN"), "PostgreSQL DSN for --reset (env RELEASE_MANAGER_DATABASE_DSN)")
	flag.Parse()

	// The fixture version is authoritative from the devseed constant; the
	// lifecycle module resolves its own copy from this flag (AC-065-30).
	if printFixtureVersion {
		fmt.Println(devfixture.DefaultFixtureVersion)
		return
	}

	cfg.Mode = devfixture.Mode(envOr("DEV_PROFILE", "local"))
	if cfg.Mode != devfixture.ModeLocal && cfg.Mode != devfixture.ModeCI {
		fail(fmt.Sprintf("invalid DEV_PROFILE %q (local or ci)", cfg.Mode))
	}
	// AC-065-28: DEV_TIMEOUT_* overrides arrive from the lifecycle module;
	// zero keeps the package defaults (180s operator wait / 3 retries).
	if operatorTimeoutSecs > 0 {
		cfg.OperatorOnlineTimeout = time.Duration(operatorTimeoutSecs) * time.Second
	}
	if seedRetries > 0 {
		cfg.SeedRetries = seedRetries
	}
	cfg.Logger = slog.New(slog.NewTextHandler(os.Stdout, nil))

	ctx := context.Background()
	var (
		manifest *devfixture.Manifest
		err      error
	)
	if resetMode {
		manifest, err = devfixture.Reset(ctx, cfg)
	} else {
		manifest, err = devfixture.Run(ctx, cfg)
	}
	if err != nil {
		slog.Error("development seed failed", "error", err)
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
	fmt.Println("development seed complete")
	if manifest != nil {
		fmt.Printf("fixture_version: %s\n", manifest.FixtureVersion)
	}
	fmt.Printf("namespace: %s\n", developmentNamespace)
	fmt.Printf("orchestrator: %s\n", strings.TrimRight(cfg.OrchestratorURL, "/"))
	fmt.Printf("webhook: %s\n", strings.TrimRight(cfg.WebhookURL, "/"))
	fmt.Printf("auth: %s\n", strings.TrimRight(cfg.AuthURL, "/"))
}

var resetMode bool

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "error:", message)
	os.Exit(1)
}
