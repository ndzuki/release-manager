package devfixture

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx-backed database/sql driver for PostgreSQL
	"github.com/ndzuki/release-manager/internal/postgres"
	"github.com/ndzuki/release-manager/migrations"
)

// Migrations is the schema-migration seam used by Reset. Production uses
// postgresMigrations; tests inject a fake to exercise the reset state
// machine without a live PostgreSQL instance.
type Migrations interface {
	// DownAll rolls every applied migration down to the baseline.
	DownAll(ctx context.Context) error
	// Up applies all pending migrations.
	Up(ctx context.Context) error
	// Close releases the database handle.
	Close() error
}

// NewPostgresMigrations opens the PostgreSQL database at dsn and returns the
// migration seam backed by internal/postgres RunMigrations/RunMigrationsDown
// over the embedded migration FS (SDK-only — no pg_dump/pg_restore/kubectl
// here; the shell layer owns those).
func NewPostgresMigrations(dsn string) (Migrations, error) {
	if dsn == "" {
		return nil, fmt.Errorf("reset requires a database DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &postgresMigrations{db: db}, nil
}

type postgresMigrations struct {
	db *sql.DB
}

func (m *postgresMigrations) DownAll(ctx context.Context) error {
	if m.db == nil {
		return fmt.Errorf("migration_failed: nil database")
	}
	var version int
	err := m.db.QueryRowContext(ctx, `SELECT version FROM schema_migrations LIMIT 1`).Scan(&version)
	if err == sql.ErrNoRows || (err == nil && version <= 0) {
		// Fresh or empty database: nothing to roll back.
		return nil
	}
	if err != nil {
		// An absent schema_migrations table means no migrations applied yet.
		if strings.Contains(err.Error(), `relation "schema_migrations" does not exist`) {
			return nil
		}
		return fmt.Errorf("migration_failed: inspect current version: %w", err)
	}
	if err := postgres.RunMigrationsDown(ctx, m.db, migrations.FS, version); err != nil {
		return fmt.Errorf("migrate down to baseline: %w", err)
	}
	return nil
}

func (m *postgresMigrations) Up(ctx context.Context) error {
	if err := postgres.RunMigrations(ctx, m.db, migrations.FS); err != nil {
		return fmt.Errorf("migrate up to latest: %w", err)
	}
	return nil
}

func (m *postgresMigrations) Close() error {
	if m.db == nil {
		return nil
	}
	return m.db.Close()
}

// reset rebuilds the databases and re-seeds every phase (force rebuild,
// fixture version ignored). The progress file is marked partial before any
// destructive step and the marker survives failures so the shell layer can
// distinguish an interrupted reset (REQ-065 partial state).
func (r *runner) reset(ctx context.Context) (*Manifest, error) {
	if err := r.loadProgress(); err != nil {
		return nil, err
	}
	r.progress = newProgress(r.cfg.FixtureVersion)
	r.progress.Partial = true
	if err := r.saveProgress(); err != nil {
		return nil, err
	}

	mg, err := r.cfg.MigrationsFactory(r.cfg.DatabaseDSN)
	if err != nil {
		return nil, fmt.Errorf("open migrations: %w", err)
	}
	defer func() { _ = mg.Close() }() //nolint:errcheck // best-effort close

	if err := mg.DownAll(ctx); err != nil {
		return nil, fmt.Errorf("reset down-all: %w", err)
	}
	if err := mg.Up(ctx); err != nil {
		return nil, fmt.Errorf("reset up: %w", err)
	}

	// The database generation changed: enrollment tokens were consumed by
	// the previous operator identities and the manifest ids are stale.
	if err := r.resetLocalRuntimeState(); err != nil {
		return nil, err
	}

	if err := r.authenticate(ctx); err != nil {
		r.markPartial()
		return nil, err
	}
	r.manifest = nil
	if err := r.runPhases(ctx); err != nil {
		r.markPartial()
		return nil, err
	}
	r.progress.Partial = false
	if err := r.saveProgress(); err != nil {
		return nil, err
	}
	return r.manifest, nil
}

// runPhases executes every non-committed phase in order (shared by run and
// reset).
func (r *runner) runPhases(ctx context.Context) error {
	phases := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "identity", run: r.phaseIdentity},
		{name: "routing", run: r.phaseRouting},
		{name: "accounts", run: r.phaseAccounts},
		{name: "trust", run: r.phaseTrust},
		{name: "bundle", run: r.phaseBundle},
		{name: "values", run: r.phaseValues},
		{name: "enrollment", run: r.phaseEnrollment},
		{name: "install", run: r.phaseInstall},
		{name: "verify", run: r.phaseVerify},
	}
	for _, phase := range phases {
		if _, committed := r.progress.Phases[phase.name]; committed {
			continue
		}
		if err := phase.run(ctx); err != nil {
			return fmt.Errorf("%s phase: %w", phase.name, err)
		}
		state := phaseState{Status: "committed", CommittedAt: r.cfg.nowRFC3339()}
		switch phase.name {
		case "install":
			state.Operations = r.state.operations
		case "bundle":
			state.BundleID = r.state.bundle.id
			state.BundleDigest = r.state.bundle.digest
		}
		r.progress.Phases[phase.name] = state
		if err := r.saveProgress(); err != nil {
			return err
		}
		r.cfg.log().Info("phase committed", "phase", phase.name)
	}
	return nil
}
