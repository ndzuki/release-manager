// Package main provides the store-migrate CLI for one-shot SQLite-to-PostgreSQL
// data migration. It reads a SQLite source file, applies PostgreSQL migrations,
// copies data, and writes a JSON report to stdout on success.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ndzuki/release-manager/internal/migration"
	"github.com/ndzuki/release-manager/internal/postgres"
)

const envTargetDSN = "RELEASE_MANAGER_DATABASE_DSN"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type exitCode int

const (
	exitOK    exitCode = 0
	exitError exitCode = 1
)

// run is the testable seam. It parses flags, performs the migration, and
// writes results to the provided writers. Returns an exit code compatible
// with os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("store-migrate", flag.ContinueOnError)
	source := flags.String("source", "", "SQLite source file path (required)")
	targetDSN := flags.String("target-dsn", os.Getenv(envTargetDSN),
		"PostgreSQL target DSN (required; env: "+envTargetDSN+")")
	migrationsDir := flags.String("migrations", "migrations",
		"directory containing PostgreSQL migration files")

	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stderr, "usage_error: %s\n", redactDSN(err.Error()))
		return int(exitError)
	}

	if *source == "" {
		fmt.Fprintln(stderr, "usage_error: --source is required")
		return int(exitError)
	}
	if *targetDSN == "" {
		fmt.Fprintf(stderr, "usage_error: --target-dsn is required (set %s env or pass --target-dsn)\n", envTargetDSN)
		return int(exitError)
	}

	migrationFS, err := postgres.LoadMigrationFS(*migrationsDir)
	if err != nil {
		fmt.Fprintf(stderr, "migration_failed: %s\n", redactDSN(err.Error()))
		return int(exitError)
	}

	cfg := migration.Config{
		SourceDSN:   *source,
		TargetDSN:   *targetDSN,
		MigrationFS: migrationFS,
	}

	report, err := migration.Run(context.Background(), cfg)
	if err != nil {
		code := classifyError(err)
		msg := stripErrorPrefix(err.Error(), code)
		fmt.Fprintf(stderr, "%s: %s\n", code, redactDSN(msg))
		return int(exitError)
	}

	fmt.Fprintln(stdout, string(report))
	return int(exitOK)
}

// classifyError extracts the stable error category prefix from a migration error.
// Categories: connection_unavailable, migration_failed, data_import_mismatch.
// Falls back to data_import_mismatch for unrecognized formats.
func classifyError(err error) string {
	msg := err.Error()
	for _, prefix := range []string{"connection_unavailable", "migration_failed", "data_import_mismatch"} {
		if strings.HasPrefix(msg, prefix) {
			return prefix
		}
	}
	return "data_import_mismatch"
}

// stripErrorPrefix removes the error category prefix (e.g. "data_import_mismatch: ")
// from the message if present, so the CLI doesn't double-output it.
func stripErrorPrefix(msg, prefix string) string {
	fullPrefix := prefix + ": "
	if after, ok := strings.CutPrefix(msg, fullPrefix); ok {
		return after
	}
	return msg
}

// redactDSN strips PostgreSQL connection URLs from error messages to prevent
// DSN leakage to stderr/logs. It replaces postgres://... and postgresql://...
// patterns with a placeholder.
func redactDSN(msg string) string {
	// Replace full DSN URLs: postgres://user:pass@host:port/db?params
	for {
		idx := strings.Index(msg, "postgresql://")
		if idx < 0 {
			idx = strings.Index(msg, "postgres://")
		}
		if idx < 0 {
			break
		}
		end := idx + 1
		for end < len(msg) && msg[end] != ' ' && msg[end] != '\n' && msg[end] != '"' && msg[end] != '\'' {
			end++
		}
		msg = msg[:idx] + "[DSN REDACTED]" + msg[end:]
	}
	return msg
}
