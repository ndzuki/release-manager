package main_test

import (
	"bytes"
	"context"
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
	command := exec.CommandContext(context.Background(), "go", "build", "-buildvcs=false", "-o", binary, ".")
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

// TestCLIRunFailsClosedAgainstAnUnreachableEnvironment verifies that the wired
// canonical stage graph reports an honest failure (exit 1) against a dead
// environment instead of a vacuous pass, while still persisting the baseline
// and per-stage artifacts and keeping stdout human-only (TASK-066 fail-closed
// contract; no fake green runs).
func TestCLIRunFailsClosedAgainstAnUnreachableEnvironment(t *testing.T) {
	configPath := writeConfig(t)
	outputDir := t.TempDir()
	result := runCLI(t, map[string]string{"E2E_RUN_ID": "cli-failclosed"},
		"run",
		"--env-config", configPath,
		"--output-dir", outputDir,
		"--stages", "artifact,control-plane",
		"--timeout", "10s",
		"--total-timeout", "20s",
		"--parallel",
		"--keep-on-failure",
		"--snapshot-full",
	)

	// There is no live environment: the control-plane observer must report the
	// unreachable endpoints and the stage must fail on them. A green exit here
	// would be a fabricated pass and is a regression.
	if result.code != 1 {
		t.Fatalf("exit code = %d, want 1 (fail closed against a dead environment); stderr=%s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "E2E run cli-failclosed") {
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
		Fail           int      `json:"fail"`
		Skip           int      `json:"skip"`
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
	// control-plane fails on the unreachable environment; artifact depends on it
	// and is therefore stage_skipped with the dependency reason (AC-066-01/41).
	if artifact.Pass != 0 || artifact.Fail != 1 || artifact.Skip != 1 || artifact.ExitCode != 1 {
		t.Fatalf("run artifact = %+v, want fail=1 skip=1 exit 1", artifact)
	}

	var stage struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
		RootCause string `json:"root_cause"`
	}

	stageData, err := os.ReadFile(filepath.Join(outputDir, "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stageData, &stage); err != nil {
		t.Fatal(err)
	}
	if stage.Status != "fail" {
		t.Fatalf("control-plane stage artifact = %+v, want a fail", stage)
	}
	// The stable stage code is asserted through root_cause rather than
	// error_code on purpose: *stages.StageError exposes its code as a field,
	// while test/e2e/runner.go duck-types a Code() string method, so only
	// *NotImplementedError is mirrored into error_code today. Asserting the code
	// text keeps this test honest and still passes once that classification is
	// fixed.
	if !strings.Contains(stage.RootCause, "environment_unhealthy") {
		t.Fatalf("control-plane root cause = %q, want the environment_unhealthy code", stage.RootCause)
	}
	if stage.ErrorCode == "" {
		t.Fatalf("control-plane stage artifact = %+v, want a machine-readable error code", stage)
	}

	stageData, err = os.ReadFile(filepath.Join(outputDir, "artifact.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stageData, &stage); err != nil {
		t.Fatal(err)
	}
	if stage.Status != "skip" || stage.ErrorCode != "stage_skipped" ||
		!strings.Contains(stage.RootCause, "dependency control-plane fail") {
		t.Fatalf("artifact stage artifact = %+v, want skip with dependency reason", stage)
	}
}

// TestCLIRunFailsClosedWithoutUsableKubeconfig verifies that a config whose
// kubeconfig cannot be read fails the run before any artifact is written: the
// canonical graph needs the typed clientset, and a graph that cannot be
// assembled must never produce a run directory that looks like a completed run.
func TestCLIRunFailsClosedWithoutUsableKubeconfig(t *testing.T) {
	configPath := writeConfigWithKubeconfig(t, filepath.Join(t.TempDir(), "absent-kubeconfig.yaml"))
	outputDir := t.TempDir()
	result := runCLI(t, map[string]string{"E2E_RUN_ID": "cli-no-kubeconfig"},
		"run",
		"--env-config", configPath,
		"--output-dir", outputDir,
		"--stages", "control-plane",
		"--timeout", "1s",
		"--total-timeout", "2s",
	)

	if result.code != 1 {
		t.Fatalf("exit code = %d, want 1 (graph assembly must fail closed); stderr=%s", result.code, result.stderr)
	}
	if !strings.Contains(result.stderr, "assemble e2e stage graph") {
		t.Fatalf("stderr = %q, want the graph assembly diagnostic", result.stderr)
	}
	for _, name := range []string{"run.json", "baseline.json", "control-plane.json"} {
		if _, err := os.Stat(filepath.Join(outputDir, name)); !os.IsNotExist(err) {
			t.Fatalf("artifact %s exists after a graph assembly failure: %v", name, err)
		}
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
	t.Cleanup(func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			t.Errorf("release lock: %v", releaseErr)
		}
	})

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

func TestCLICleanupUsesBaselineOverrideAndFailsClosedWithoutLiveEnv(t *testing.T) {
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
	// There is no live dev environment in unit tests: the cleanup subcommand
	// validates the config seam and baseline file, then fails closed when the
	// e2e-runner login cannot reach the Auth endpoint. A nonzero exit is the
	// correct outcome; it must never silently claim a best-effort success.
	if result.code != 1 {
		t.Fatalf("exit code = %d, want 1 (cleanup must fail closed without a live environment); stderr=%s", result.code, result.stderr)
	}
	if !strings.Contains(result.stderr, "cleanup is residual-only") {
		t.Fatalf("stderr = %q, want baseline degradation warning", result.stderr)
	}
	if !strings.Contains(result.stdout, "E2E cleanup failed") {
		t.Fatalf("stdout = %q, want fail-closed summary", result.stdout)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "run.json")); !os.IsNotExist(err) {
		t.Fatalf("run.json exists after cleanup: %v", err)
	}
}

type cliResult struct {
	code   int
	stdout string
	stderr string
}

func runCLI(t *testing.T, env map[string]string, args ...string) cliResult {
	t.Helper()
	command := exec.CommandContext(context.Background(), e2eBinary, args...)
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
	return writeConfigWithKubeconfig(t, writeTestKubeconfig(t))
}

// writeTestKubeconfig writes a syntactically valid kubeconfig so the CLI can
// assemble the canonical stage graph without a cluster: client-go parses the
// config eagerly and connects lazily.
func writeTestKubeconfig(t *testing.T) string {
	t.Helper()

	const body = `apiVersion: v1
kind: Config
clusters:
  - name: cli-test
    cluster:
      server: https://127.0.0.1:6443
contexts:
  - name: cli-test
    context:
      cluster: cli-test
      user: cli-test
current-context: cli-test
users:
  - name: cli-test
    user:
      token: cli-test-token
`
	path := filepath.Join(t.TempDir(), "kubeconfig.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

func writeConfigWithKubeconfig(t *testing.T, kubeconfig string) string {
	t.Helper()
	t.Setenv("E2E_RUNNER_PASSWORD", "test-password")
	path := filepath.Join(t.TempDir(), "env-config.yaml")
	config := fmt.Sprintf(`environment: ci
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
  kubeconfig: %s
  test_namespace: release-manager-dev
  restart_targets:
    namespace: release-manager-dev
    deployments: [auth, orchestrator, webhook]
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
    e2e_definition_ids: [11111111-1111-1111-1111-111111111111, 22222222-2222-2222-2222-222222222222, 33333333-3333-3333-3333-333333333333, 44444444-4444-4444-4444-444444444444]
  e2e_upgrade_targets:
    - logical_key: e2e-release-target
      definition_id: 11111111-1111-1111-1111-111111111111
      bundle_id: bundle-1
      values_revision_id: values-1
    - logical_key: e2e-isolation-target
      definition_id: 22222222-2222-2222-2222-222222222222
      bundle_id: bundle-1
      values_revision_id: values-1
    - logical_key: e2e-restart-target
      definition_id: 44444444-4444-4444-4444-444444444444
      bundle_id: bundle-1
      values_revision_id: values-1
  e2e_emergency_definition_id: 33333333-3333-3333-3333-333333333333
`, kubeconfig)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
