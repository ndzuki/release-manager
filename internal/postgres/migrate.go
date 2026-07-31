package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// MigrationSource is the filesystem containing ordered *.up.sql and *.down.sql files.
const MigrationSource = "migrations"

// RunMigrations runs all pending PostgreSQL migrations. ErrNoChange is success;
// dirty state and all other failures are returned as migration_failed.
func RunMigrations(ctx context.Context, db *sql.DB, migrationFS fs.FS) error {
	return runMigrations(ctx, db, migrationFS, func(m *migrate.Migrate) error {
		return m.Up()
	})
}

// RunMigrationsDown rolls back the requested number of schema versions.
// It is intended for explicit operator rollback, never normal service startup.
func RunMigrationsDown(ctx context.Context, db *sql.DB, migrationFS fs.FS, steps int) error {
	if steps <= 0 {
		return fmt.Errorf("migration_failed: down steps must be positive")
	}
	return runMigrations(ctx, db, migrationFS, func(m *migrate.Migrate) error {
		return m.Steps(-steps)
	})
}

func runMigrations(ctx context.Context, db *sql.DB, migrationFS fs.FS, apply func(*migrate.Migrate) error) error {
	if db == nil {
		return fmt.Errorf("migration_failed: nil database")
	}
	entries, err := fs.ReadDir(migrationFS, ".")
	if err != nil {
		return fmt.Errorf("migration_failed: read migration source: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("migration_failed: migration source is empty")
	}
	if err := validateMigrationNames(entries); err != nil {
		return err
	}
	if err := repairHistoricalMigrationVersion(ctx, db); err != nil {
		return err
	}
	source, err := iofs.New(migrationFS, ".")
	if err != nil {
		return fmt.Errorf("migration_failed: initialize migration source: %w", err)
	}
	defer func() { _ = source.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migration_failed: acquire migration connection: %w", err)
	}
	driver, err := migratepostgres.WithConnection(ctx, conn, &migratepostgres.Config{})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("migration_failed: initialize PostgreSQL driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		_ = driver.Close()
		return fmt.Errorf("migration_failed: initialize migration runner: %w", err)
	}
	defer func() { _, _ = m.Close() }()
	if err := apply(m); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migration_failed: apply migrations: %w", err)
	}
	return nil
}

type migrationFeatures struct {
	bundle    bool
	emergency bool
	upgrade   bool
	timeline  bool
}

// repairHistoricalMigrationVersion handles the period where independently
// developed migrations reused versions 7 and 8. golang-migrate stores only an
// integer version, so the schema itself is the authority for selecting the
// earliest safe replay point. Replayed migrations are intentionally made
// idempotent (IF NOT EXISTS, ADD COLUMN IF NOT EXISTS).
func repairHistoricalMigrationVersion(ctx context.Context, db *sql.DB) error {
	var version int
	var dirty bool
	err := db.QueryRowContext(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil && strings.Contains(err.Error(), `relation "schema_migrations" does not exist`) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("migration_failed: inspect current version: %w", err)
	}
	if dirty || version < 7 {
		return nil
	}
	features, err := inspectMigrationFeatures(ctx, db)
	if err != nil {
		return err
	}
	rewind := historicalMigrationRewind(version, features)
	if rewind >= version {
		return nil
	}
	if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET version = $1, dirty = FALSE`, rewind); err != nil {
		return fmt.Errorf("migration_failed: repair historical version %d to %d: %w", version, rewind, err)
	}
	return nil
}

func inspectMigrationFeatures(ctx context.Context, db *sql.DB) (migrationFeatures, error) {
	var features migrationFeatures
	err := db.QueryRowContext(ctx, `
		SELECT
			to_regclass('release_bundle_image_bindings') IS NOT NULL,
			to_regclass('emergency_intents') IS NOT NULL,
			to_regclass('operation_execution_results') IS NOT NULL,
			to_regclass('operation_timeline') IS NOT NULL
	`).Scan(&features.bundle, &features.emergency, &features.upgrade, &features.timeline)
	if err != nil {
		return migrationFeatures{}, fmt.Errorf("migration_failed: inspect historical schema: %w", err)
	}
	return features, nil
}

func historicalMigrationRewind(version int, features migrationFeatures) int {
	switch {
	case version >= 7 && !features.bundle:
		return 6
	case version >= 8 && !features.emergency:
		return 7
	case version >= 9 && !features.upgrade:
		return 8
	case version >= 10 && !features.timeline:
		return 9
	default:
		return version
	}
}

//nolint:gocyclo // Filename validation keeps duplicate, pairing, and contiguity errors distinct.
func validateMigrationNames(entries []fs.DirEntry) error {
	type directions struct {
		up   bool
		down bool
	}
	versions := make(map[int]directions)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		var direction string
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			direction = "up"
		case strings.HasSuffix(name, ".down.sql"):
			direction = "down"
		default:
			continue
		}
		prefix := strings.SplitN(name, "_", 2)[0]
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 {
			return fmt.Errorf("migration_failed: invalid migration filename %q", name)
		}
		seen := versions[version]
		if direction == "up" {
			if seen.up {
				return fmt.Errorf("migration_failed: duplicate up migration version %d", version)
			}
			seen.up = true
		} else {
			if seen.down {
				return fmt.Errorf("migration_failed: duplicate down migration version %d", version)
			}
			seen.down = true
		}
		versions[version] = seen
	}
	if len(versions) == 0 {
		return fmt.Errorf("migration_failed: no SQL migrations found")
	}
	ordered := make([]int, 0, len(versions))
	for version := range versions {
		ordered = append(ordered, version)
	}
	sort.Ints(ordered)
	for i, version := range ordered {
		if version != i+1 {
			return fmt.Errorf("migration_failed: migration versions must be contiguous from 1")
		}
		directions := versions[version]
		if !directions.up || !directions.down {
			return fmt.Errorf("migration_failed: version %d requires both up and down migrations", version)
		}
	}
	return nil
}

// LoadMigrationFS loads migrations from a filesystem directory for CLI/tests.
func LoadMigrationFS(path string) (fs.FS, error) {
	if path == "" {
		path = MigrationSource
	}
	if _, err := os.Stat(filepath.Clean(path)); err != nil {
		return nil, fmt.Errorf("migration_failed: stat migration directory: %w", err)
	}
	return os.DirFS(filepath.Clean(path)), nil
}
