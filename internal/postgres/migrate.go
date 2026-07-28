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
