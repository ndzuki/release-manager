package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// AC-066-24: omitting --stages and --output-dir must be equivalent to
// "--stages all" writing into ./e2e-results, created with its parents.
func TestParseRunOptionsDefaults(t *testing.T) {
	options, err := parseRunOptions([]string{"--env-config", "e2e.yaml"}, io.Discard)
	if err != nil {
		t.Fatalf("parseRunOptions: %v", err)
	}
	if options.stages != "all" {
		t.Fatalf("default stages = %q, want all", options.stages)
	}
	if options.outputDir != "./e2e-results" {
		t.Fatalf("default output dir = %q, want ./e2e-results", options.outputDir)
	}
}

// AC-066-24: the output directory is created, including missing parents.
func TestPrepareOutputDirCreatesMissingParents(t *testing.T) {
	nested := filepath.Join(t.TempDir(), "results", "nested", "run")

	if err := prepareOutputDir(nested, []string{"inventory"}); err != nil {
		t.Fatalf("prepareOutputDir: %v", err)
	}
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("the output directory must exist with its parents: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", nested)
	}
}

// An explicit empty --output-dir is an error rather than a silent fallback.
func TestPrepareOutputDirRejectsEmpty(t *testing.T) {
	if err := prepareOutputDir("  ", []string{"inventory"}); err == nil {
		t.Fatal("an empty output dir must be rejected")
	}
}
