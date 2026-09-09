package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndzuki/release-manager/test/e2e"
)

var e2eBinary string

func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		os.Exit(m.Run())
	}
	binary := filepath.Join(os.TempDir(), fmt.Sprintf("release-manager-e2e-%d", os.Getpid()))
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build e2e CLI: %v\n", err)
		os.Exit(1)
	}
	e2eBinary = binary
	code := m.Run()
	_ = os.Remove(binary)
	os.Exit(code)
}

func TestCLIRejectsMissingConfigWithoutRunArtifact(t *testing.T) {
	outputDir := t.TempDir()
	result := runCLI(t, nil,
		"--env-config", filepath.Join(outputDir, "missing.yaml"),
		"--output-dir", outputDir,
	)

	if result.code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", result.code, result.stderr)
	}
	if result.stdout != "" {
		t.Fatalf("stdout = %q, want empty", result.stdout)
	}
	if !strings.Contains(result.stderr, "usage: e2e") {
		t.Fatalf("stderr = %q, want usage", result.stderr)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "run.json")); !os.IsNotExist(err) {
		t.Fatalf("run.json exists after config failure: %v", err)
	}
}

func TestCLIRejectsDuplicateStagesWithoutRunArtifact(t *testing.T) {
	configPath := writeConfig(t)
	outputDir := t.TempDir()
	result := runCLI(t, map[string]string{"E2E_RUN_ID": "cli-duplicate"},
		"--env-config", configPath,
		"--output-dir", outputDir,
		"--stages", "inventory,inventory",
	)

	if result.code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", result.code, result.stderr)
	}
	if !strings.Contains(result.stderr, "invalid stage selection") {
		t.Fatalf("stderr = %q, want invalid stage selection", result.stderr)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "run.json")); !os.IsNotExist(err) {
		t.Fatalf("run.json exists after stage validation failure: %v", err)
	}
}

func TestCLIRunKeepsStdoutHumanAndArtifactsInOutputDir(t *testing.T) {
	configPath := writeConfig(t)
	outputDir := t.TempDir()
	result := runCLI(t, map[string]string{"E2E_RUN_ID": "cli-success"},
		"run",
		"--env-config", configPath,
		"--output-dir", outputDir,
		"--stages", "artifact,control-plane",
		"--timeout", "1s",
		"--total-timeout", "2s",
		"--parallel",
		"--keep-on-failure",
		"--snapshot-full",
	)

	if result.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "E2E run cli-success") {
		t.Fatalf("stdout = %q, want human summary", result.stdout)
	}
	if strings.Contains(result.stdout, "\"selected_stages\"") {
		t.Fatalf("stdout contains JSON artifact data: %q", result.stdout)
	}
	if !strings.Contains(result.stderr, "level=INFO") {
		t.Fatalf("stderr = %q, want slog text diagnostics", result.stderr)
	}

	for _, name := range []string{"baseline.json", "control-plane.json", "artifact.json", "run.json"} {
		if _, err := os.Stat(filepath.Join(outputDir, name)); err != nil {
			t.Fatalf("artifact %s missing: %v", name, err)
		}
	}
	var artifact struct {
		SelectedStages []string `json:"selected_stages"`
		Pass           int      `json:"pass"`
		ExitCode       int      `json:"exit_code"`
	}
	data, err := os.ReadFile(filepath.Join(outputDir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &artifact); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(artifact.SelectedStages, ","), "control-plane,artifact"; got != want {
		t.Fatalf("selected stages = %q, want %q", got, want)
	}
	if artifact.Pass != 2 || artifact.ExitCode != 0 {
		t.Fatalf("run artifact = %+v, want two passes and exit 0", artifact)
	}
}

func TestCLIPreservesLockConflictExitCode(t *testing.T) {
	configPath := writeConfig(t)
	outputDir := t.TempDir()
	lockPath := filepath.Join(t.TempDir(), "dev.lock")
	lock, err := e2e.AcquireProcessLock(lockPath, "test-owner")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()

	result := runCLI(t, map[string]string{
		"E2E_RUN_ID":    "cli-locked",
		"E2E_LOCK_FILE": lockPath,
	}, "--env-config", configPath, "--output-dir", outputDir)
	if result.code != 3 {
		t.Fatalf("exit code = %d, want 3; stderr=%s", result.code, result.stderr)
	}
	if !strings.Contains(result.stderr, "environment_locked") {
		t.Fatalf("stderr = %q, want environment_locked", result.stderr)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "run.json")); !os.IsNotExist(err) {
		t.Fatalf("run.json exists after lock conflict: %v", err)
	}
}

func TestCLICleanupUsesBaselineOverride(t *testing.T) {
	configPath := writeConfig(t)
	outputDir := t.TempDir()
	baseline := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(baseline, []byte(`{"run_id":"old-run"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	result := runCLI(t, map[string]string{"E2E_RUN_ID": "cli-cleanup"},
		"cleanup",
		"--env-config", configPath,
		"--output-dir", outputDir,
		"--baseline-file", baseline,
	)
	if result.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, baseline) {
		t.Fatalf("stdout = %q, want baseline path", result.stdout)
	}
	if !strings.Contains(result.stderr, "baseline loaded for cleanup") {
		t.Fatalf("stderr = %q, want cleanup log", result.stderr)
	}
}

type cliResult struct {
	code   int
	stdout string
	stderr string
}

func runCLI(t *testing.T, env map[string]string, args ...string) cliResult {
	t.Helper()
	command := exec.Command(e2eBinary, args...)
	command.Dir = filepath.Dir(e2eBinary)
	command.Env = append([]string{}, os.Environ()...)
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func writeConfig(t *testing.T) string {
	t.Helper()
	t.Setenv("E2E_RUNNER_PASSWORD", "test-password")
	path := filepath.Join(t.TempDir(), "env-config.yaml")
	const config = `environment: ci
environment_id: ci-run
endpoints:
  release_orchestrator: http://localhost:8083
  release_webhook: http://localhost:8082
  release_operator: https://localhost:8084
  release_auth: http://localhost:8085
  release_notifier: http://localhost:8086
  release_api: http://localhost:8087
credentials:
  e2e_runner:
    username: e2e-runner
    password_env: E2E_RUNNER_PASSWORD
k3d:
  kubeconfig: /tmp/kubeconfig
  test_namespace: release-manager-dev
  restart_targets:
    namespace: release-manager-dev
    deployments: [auth, operator-gateway, orchestrator]
seed:
  customers: [dev-customer-a]
  clusters_per_customer: 1
  fixture_version: fixture-v1
  expected_identity:
    customers: 1
    clusters: 1
    routes_basic: 0
    definitions_basic: 0
    bundles: 1
    e2e_definition_ids: [e2e-release-target, e2e-isolation-target, e2e-emergency-target, e2e-restart-target]
  e2e_upgrade_targets:
    - definition_id: e2e-release-target
      bundle_id: bundle-1
      values_revision_id: values-1
    - definition_id: e2e-isolation-target
      bundle_id: bundle-1
      values_revision_id: values-1
    - definition_id: e2e-restart-target
      bundle_id: bundle-1
      values_revision_id: values-1
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
