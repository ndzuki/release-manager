package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRun_MissingSource(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr)
	assert.Equal(t, int(exitError), code)
	output := stderr.String()
	assert.Contains(t, output, "usage_error")
	assert.Contains(t, output, "--source is required")
	assert.Empty(t, stdout.String())
}

func TestRun_MissingTargetDSN(t *testing.T) {
	t.Setenv("RELEASE_MANAGER_DATABASE_DSN", "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--source", "/nonexistent/db.sqlite"}, &stdout, &stderr)
	assert.Equal(t, int(exitError), code)
	output := stderr.String()
	assert.Contains(t, output, "usage_error")
	assert.Contains(t, output, "--target-dsn")
	assert.Empty(t, stdout.String())
}

func TestRun_TargetDSNFromEnv(t *testing.T) {
	t.Setenv("RELEASE_MANAGER_DATABASE_DSN", "postgres://user:pass@localhost:5432/db")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--source", "/nonexistent/db.sqlite"}, &stdout, &stderr)
	assert.Equal(t, int(exitError), code)
	output := stderr.String()
	assert.NotContains(t, output, "--target-dsn")
	assert.Empty(t, stdout.String())
}

func TestRun_InvalidFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--nonexistent-flag"}, &stdout, &stderr)
	assert.Equal(t, int(exitError), code)
	output := stderr.String()
	assert.Contains(t, output, "usage_error")
	assert.Empty(t, stdout.String())
}

func TestRun_SourceNotAccessible(t *testing.T) {
	t.Setenv("RELEASE_MANAGER_DATABASE_DSN", "postgres://user:pass@localhost:5432/db")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"--source", "/nonexistent/path/db.sqlite",
			"--migrations", "../../migrations",
			"--target-dsn", "postgres://user:pass@localhost:5432/db",
		},
		&stdout,
		&stderr,
	)
	assert.Equal(t, int(exitError), code)
	output := stderr.String()
	// Source file not found → classified as data_import_mismatch.
	assert.Contains(t, output, "data_import_mismatch")
	assert.Empty(t, stdout.String())
}

func TestRun_MigrationsDirNotFound(t *testing.T) {
	t.Setenv("RELEASE_MANAGER_DATABASE_DSN", "postgres://user:pass@localhost:5432/db")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"--source", "/nonexistent/db.sqlite",
			"--migrations", "/nonexistent/migrations",
			"--target-dsn", "postgres://user:pass@localhost:5432/db",
		},
		&stdout,
		&stderr,
	)
	assert.Equal(t, int(exitError), code)
	output := stderr.String()
	assert.Contains(t, output, "migration_failed")
	assert.Empty(t, stdout.String())
}

func TestRun_DSNNotLeaked(t *testing.T) {
	t.Setenv("RELEASE_MANAGER_DATABASE_DSN", "")
	dsn := "postgresql://admin:s3cret@db.example.com:5432/release_manager?sslmode=require"

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"--source", "/nonexistent/db.sqlite",
			"--migrations", "../../migrations",
			"--target-dsn", dsn,
		},
		&stdout,
		&stderr,
	)
	assert.Equal(t, int(exitError), code)
	output := stderr.String()
	assert.NotContains(t, output, dsn)
	assert.NotContains(t, output, "s3cret")
	assert.NotContains(t, output, "postgresql://")
	assert.NotContains(t, output, "postgres://")
	assert.Empty(t, stdout.String())
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"connection_unavailable: ping target: timeout", "connection_unavailable"},
		{"migration_failed: apply migrations: dirty state", "migration_failed"},
		{"data_import_mismatch: table customers row count mismatch", "data_import_mismatch"},
		{"data_import_mismatch: open source: file not found", "data_import_mismatch"},
		{"unknown error without prefix", "data_import_mismatch"},
	}
	for _, tt := range tests {
		got := classifyError(&stringError{msg: tt.input})
		assert.Equal(t, tt.expected, got, "classifyError(%q)", tt.input)
	}
}

func TestRedactDSN(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello world", "hello world"},
		{"connect postgres://user:pass@host/db failed", "connect [DSN REDACTED] failed"},
		{"postgresql://admin:s3cret@db.example.com:5432/release_manager?sslmode=require",
			"[DSN REDACTED]"},
		{"error: postgres://u:p@localhost/db and more", "error: [DSN REDACTED] and more"},
		{"connection_unavailable: ping target: dial tcp 192.0.2.1:5432: i/o timeout",
			"connection_unavailable: ping target: dial tcp 192.0.2.1:5432: i/o timeout"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, redactDSN(tt.input), "redactDSN(%q)", tt.input)
	}
}

type stringError struct{ msg string }

func (e *stringError) Error() string { return e.msg }
