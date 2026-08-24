// Package devtest exercises the deploy/dev lifecycle module against fake
// CLIs. The tests assert error codes, exit codes, lock semantics and
// ownership gating through the module's public surface (REQ-065 AC-065-05/06/
// 07/11/14/20/21/22/23/25), without requiring Docker or k3d on the host.
package devtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// repoRoot resolves the repository root from the test working directory
// (go test runs per-package, so the cwd is deploy/dev).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test cwd")
		}
		dir = parent
	}
}

// fakeEnv builds an environment that points the lifecycle module at fake
// CLI shims and an isolated data dir.
func fakeEnv(t *testing.T, stateDir string) (env []string, binDir string) {
	t.Helper()
	binDir = filepath.Join(stateDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env = append(os.Environ(),
		"DEV_DATA_DIR="+stateDir,
		// Probe idle test ports, not the real 8082-8087: the actual dev
		// environment may be running on the host during tests.
		"DEV_PORTS_OVERRIDE=19082 19083 19084 19085 19086 19087",
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	return env, binDir
}

// writeShim writes an executable fake CLI.
func writeShim(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runDev runs deploy/dev/dev.sh with the given env; returns combined output.
func runDev(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	root := repoRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(root, "deploy", "dev", "dev.sh"), args...)
	cmd.Dir = root
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// exitCode extracts the process exit code from an exec error.
func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	return exitErr.ExitCode()
}

func TestLockConflictReportsHolder(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	writeShim(t, binDir, "k3d", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "docker", "#!/usr/bin/env bash\nexit 0\n")
	// (down is a no-op on an empty manifest and would release the lock
	// before the second process starts).
	holdCtx, holdCancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer holdCancel()
	hold := exec.CommandContext(holdCtx, "bash", "-c", fmt.Sprintf(
		"source %q; acquire_lock down; sleep 30",
		filepath.Join(repoRoot(t), "deploy", "dev", "lib", "lock.sh")))
	hold.Env = env
	if err := hold.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		//nolint:errcheck // test teardown: process may already be gone
		hold.Process.Kill()
		//nolint:errcheck // test teardown
		hold.Process.Wait()
	}()

	// The flock is held only after the holder wrote its stage record; the
	// lock file itself is created before acquisition and is not a reliable
	// ready signal.
	stagePath := filepath.Join(stateDir, "dev-stage.json")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stagePath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(stagePath); err != nil {
		t.Fatal("holder never acquired the lock")
	}

	out, err := runDev(t, env, "down")
	if err == nil {
		t.Fatalf("expected lock conflict, got success:\n%s", out)
	}
	if code := exitCode(t, err); code != 3 {
		t.Fatalf("expected exit 3, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "environment_locked") {
		t.Fatalf("expected environment_locked in output:\n%s", out)
	}
	if !strings.Contains(out, "pid=") || !strings.Contains(out, "started_at=") {
		t.Fatalf("expected holder pid/started_at in output:\n%s", out)
	}
}

func TestStatusUsesSharedLock(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	writeShim(t, binDir, "k3d", "#!/usr/bin/env bash\nexit 0\n")

	// Two concurrent status calls must both succeed (LOCK_SH is shared).
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := runDev(t, env, "status")
			done <- err
		}()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("shared status failed: %v", err)
		}
	}
}

func TestPurgeRequiresConfirm(t *testing.T) {
	stateDir := t.TempDir()
	env, _ := fakeEnv(t, stateDir)
	out, err := runDev(t, env, "purge")
	if err == nil {
		t.Fatalf("expected confirm gate failure:\n%s", out)
	}
	if code := exitCode(t, err); code != 2 {
		t.Fatalf("expected exit 2, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "dev_purge_confirm_required") {
		t.Fatalf("expected dev_purge_confirm_required:\n%s", out)
	}
	if !strings.Contains(out, "set CONFIRM=1 to proceed") {
		t.Fatalf("expected CONFIRM hint:\n%s", out)
	}
}

func TestResetRequiresConfirm(t *testing.T) {
	stateDir := t.TempDir()
	env, _ := fakeEnv(t, stateDir)
	out, err := runDev(t, env, "reset-data")
	if err == nil {
		t.Fatalf("expected confirm gate failure:\n%s", out)
	}
	if code := exitCode(t, err); code != 2 {
		t.Fatalf("expected exit 2, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "confirm_required") {
		t.Fatalf("expected confirm_required:\n%s", out)
	}
}

// TestPreflightBatteryOrder runs dev-up with an awk shim that reports only
// 8 GiB available and asserts the memory gate fires with a stable REQ-065
// error code before any resource creation (AC-065-20) — deterministic on
// every host, unlike the earlier host-probe version that created real k3d
// clusters once the host passed every gate. The full battery order is
// covered by fake-CLI tests in host_test.go.
func TestPreflightBatteryOrder(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	writeShim(t, binDir, "awk", "#!/usr/bin/env bash\ncat >/dev/null\nprintf '8000\\n'\n")
	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("expected preflight failure, got success:\n%s", out)
	}
	code := exitCode(t, err)
	if code != 1 && code != 2 && code != 3 {
		t.Fatalf("expected preflight failure exit, got %d\n%s", code, out)
	}
	for _, want := range []string{
		"host_memory_insufficient", "host_disk_insufficient",
		"port_conflict", "docker_unavailable", "k3d_unavailable",
	} {
		if strings.Contains(out, want) {
			return
		}
	}
	t.Fatalf("no preflight error code found in output:\n%s", out)
}

func TestCiProfileRequiresE2ERunID(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	writeShim(t, binDir, "flock", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "docker", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "k3d", "#!/usr/bin/env bash\nprintf 'k3d version v5.8.3 k3s1\\n'\n")
	writeShim(t, binDir, "curl", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "kubectl", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nexit 0\n")
	env = append(env, "DEV_PROFILE=ci")

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("expected e2e_run_id_invalid, got success:\n%s", out)
	}
	if !strings.Contains(out, "e2e_run_id_invalid") {
		t.Fatalf("expected e2e_run_id_invalid:\n%s", out)
	}
	if !strings.Contains(out, "DNS-1123") {
		t.Fatalf("expected DNS-1123 hint:\n%s", out)
	}

	// A valid run id must pass the validation gate. The run will then fail
	// on a later stage (no real docker), which is fine — the important
	// assertion is that e2e_run_id_invalid is NOT emitted.
	env = append(env, "E2E_RUN_ID=run-42")
	out, err = runDev(t, env, "up")
	if err != nil && strings.Contains(out, "e2e_run_id_invalid") {
		t.Fatalf("valid E2E_RUN_ID rejected:\n%s", out)
	}
}

// fakeK3d installs the stateful k3d shim from testdata/fake-k3d.sh: cluster
// list/create/delete, registry list/create and kubeconfig get/merge all work
// against a per-test state dir, so dev.sh up/down runs end-to-end without
// Docker or k3d on the host.
func fakeK3d(t *testing.T, binDir, stateDir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "dev", "testdata", "fake-k3d.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := "#!/usr/bin/env bash\nFAKE_K3D_STATE=\"" + stateDir + "\"\n" + string(data)
	writeShim(t, binDir, "k3d", body)
}

// happyShims installs pass-through shims for the rest of the dev-up chain:
// registry reachable, image digests already present (no build/push), apply
// and readiness succeed, seed succeeds. The host-gate shims (awk/df/nproc)
// answer the memory/disk/CPU probes so tests run on hosts below the REQ
// minimums without touching real resources.
func happyShims(t *testing.T, binDir string) {
	t.Helper()
	// docker: `container inspect` reports the object does not exist (the
	// registry container is only created by the fake k3d) EXCEPT for the
	// --format IP probes used by agents_up (management node / registry
	// hostAliases); other verbs (start/rm) pass through.
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then
  if [ "$3" = "--format" ]; then
    case "${5}" in
      k3d-release-manager-control-server-0) printf '172.18.0.2\n'; exit 0 ;;
      k3d-release-manager-registry) printf '172.18.0.3\n'; exit 0 ;;
      *) exit 1 ;;
    esac
  fi
  exit 1
fi
exit 0
`)
	writeShim(t, binDir, "curl", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "kubectl", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nexit 0\n")
	// go: answer the GOPROXY probe dev.sh forwards as a build-arg; the seed
	// path (go run ./cmd/devseed) writes the four enrollment tokens (the
	// split-seed agents_up stage consumes them) and exits 0.
	writeShim(t, binDir, "go", `#!/usr/bin/env bash
if [ "$1" = "env" ]; then printf 'https://proxy.golang.org,direct\n'; exit 0; fi
mkdir -p "$DEV_DATA_DIR/dev-enrollment-tokens"
for c in dev-customer-a-direct dev-customer-a-cache dev-customer-b-replicated dev-customer-b-mixed; do
  printf 'fake-token\n' > "$DEV_DATA_DIR/dev-enrollment-tokens/$c.token"
done
exit 0
`)
	// awk: consume stdin fully (real awk reads all input before exiting —
	// an early exit SIGPIPEs the pipe writer), then answer the memory probe
	// in MiB (24 GiB) or the df pipeline in KiB (500 GiB).
	writeShim(t, binDir, "awk", "#!/usr/bin/env bash\ncat >/dev/null\nif [[ \"$*\" == *\"/proc/meminfo\"* ]]; then printf '24576\\n'; else printf '524288000\\n'; fi\n")
	// df: 500 GiB available.
	writeShim(t, binDir, "df", "#!/usr/bin/env bash\nprintf 'Filesystem 1024-blocks Used Available Capacity Mounted on\\n/dev/x 1000000000 1 524288000 1%% /\\n'\n")
	writeShim(t, binDir, "nproc", "#!/usr/bin/env bash\nprintf '8\\n'\n")
}

// k3dCreates returns the recorded `k3d cluster create` invocations, one
// string per call: "<name>|<argv...>".
func k3dCreates(stateDir string) []string {
	data, err := os.ReadFile(filepath.Join(stateDir, "k3d-creates.log"))
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// clusterCreates filters out non-cluster lines (e.g. the fake registry
// create record) from the k3d invocation log.
func clusterCreates(records []string) []string {
	var clusters []string
	for _, r := range records {
		if !strings.HasPrefix(r, "registry create ") {
			clusters = append(clusters, r)
		}
	}
	return clusters
}

func TestDevUpCreatesFiveClustersAndMergedKubeconfig(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)

	out, err := runDev(t, env, "up")
	if err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}
	creates := clusterCreates(k3dCreates(stateDir))
	if len(creates) != 5 {
		t.Fatalf("expected 5 cluster creates, got %d: %v", len(creates), creates)
	}
	all := strings.Join(creates, "\n")
	for _, want := range []string{"--registry-use", "--registry-config", "--api-port", "--port"} {
		if !strings.Contains(all, want) {
			t.Fatalf("expected create flag %s in creates: %v", want, creates)
		}
	}
	// The loadbalancer port mapping belongs to the control cluster only.
	for _, c := range creates {
		if strings.HasPrefix(c, "release-manager-control|") && !strings.Contains(c, "8082-8087:30082-30087@loadbalancer") {
			t.Fatalf("control cluster create missing port mapping: %s", c)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "kubeconfig.yaml")); err != nil {
		t.Fatalf("expected merged data/kubeconfig.yaml, got %v", err)
	}
	// Ownership manifest lists the 5 clusters and the registry container.
	manifest, err := os.ReadFile(filepath.Join(stateDir, "dev-ownership.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"release-manager-control", "dev-customer-a-direct", "dev-customer-a-cache",
		"dev-customer-b-replicated", "dev-customer-b-mixed", "k3d-release-manager-registry",
	} {
		if !strings.Contains(string(manifest), want) {
			t.Fatalf("ownership manifest missing %s:\n%s", want, manifest)
		}
	}
}

func TestDevUpIsIdempotent(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("first dev-up failed: %v\n%s", err, out)
	}
	first := clusterCreates(k3dCreates(stateDir))
	// Simulate a partially-created environment: keep the control cluster
	// and the first customer cluster, drop the other three.
	clustersPath := filepath.Join(stateDir, "clusters.txt")
	if err := os.WriteFile(clustersPath, []byte("release-manager-control\ndev-customer-a-direct\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("second dev-up failed: %v\n%s", err, out)
	}
	second := clusterCreates(k3dCreates(stateDir))
	// Only the three missing clusters are created; the two present are skipped.
	if len(second)-len(first) != 3 {
		t.Fatalf("expected 3 creates on resume, got %d (first=%d second=%d)",
			len(second)-len(first), len(first), len(second))
	}
}

func TestClusterCreateInjectsProxyEnv(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// Record docker invocations so the build proxy injection can be
	// asserted. manifest inspect fails (registry empty) so every service
	// actually goes through docker build; build/push pass through.
	writeShim(t, binDir, "docker",
		"#!/usr/bin/env bash\nif [ \"$1\" = \"manifest\" ] && [ \"$2\" = \"inspect\" ]; then exit 1; fi\nif [ \"$1\" = \"container\" ] && [ \"$2\" = \"inspect\" ]; then exit 1; fi\nprintf '%s\\n' \"$*\" >> \""+stateDir+"/docker-calls.log\"\nexit 0\n")
	// GOPROXY is explicitly cleared so dev.sh takes the `go env GOPROXY`
	// fallback path and the shim's fixed output is asserted — otherwise an
	// inherited host GOPROXY (e.g. goproxy.cn) makes the assertion
	// environment-dependent.
	env = append(env, "HTTP_PROXY=http://127.0.0.1:7890", "HTTPS_PROXY=http://127.0.0.1:7890",
		"GOPROXY=", "DEV_DOCKER_MIRROR=docker.1ms.run/library/")

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}
	creates := clusterCreates(k3dCreates(stateDir))
	for _, c := range creates {
		if !strings.Contains(c, "NO_PROXY=k3d-release-manager-registry") {
			t.Fatalf("create missing NO_PROXY registry domain: %s", c)
		}
	}
	if len(creates) == 0 {
		t.Fatal("no cluster creates recorded")
	}
	calls, err := os.ReadFile(filepath.Join(stateDir, "docker-calls.log"))
	if err != nil {
		t.Fatalf("docker-calls.log not written: %v", err)
	}
	var buildInjected bool
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if !strings.HasPrefix(line, "build ") {
			continue
		}
		if !strings.Contains(line, "--build-arg HTTP_PROXY=http://127.0.0.1:7890") ||
			!strings.Contains(line, "--build-arg HTTPS_PROXY=http://127.0.0.1:7890") ||
			!strings.Contains(line, "--build-arg GOPROXY=https://proxy.golang.org,direct") {
			t.Fatalf("docker build missing proxy/GOPROXY build-args: %s", line)
		}
		if strings.Contains(line, "release-web:") &&
			!strings.Contains(line, "--build-arg NODE_IMAGE=docker.1ms.run/library/node:24-alpine") {
			t.Fatalf("web build missing NODE_IMAGE mirror build-arg: %s", line)
		}
		buildInjected = true
	}
	if !buildInjected {
		t.Fatalf("no docker build invocation recorded:\n%s", calls)
	}
}
func TestStatusJSONSchema(t *testing.T) {
	stateDir := t.TempDir()
	env, _ := fakeEnv(t, stateDir)

	out, err := runDev(t, env, "status")
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		`"environment_id":"dev-local"`, `"profile":"local"`,
		`"control":{"name":"release-manager-control"`,
		`"endpoints":{"webhook":"http://localhost:8082"`,
		`"operator_sessions":0`, `"fixture_entities":{"customers":0`,
		`"bootstrap_installs":0`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %s:\n%s", want, out)
		}
	}
}

func TestResetFailsWithoutRunningEnvironment(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	writeShim(t, binDir, "k3d", "#!/usr/bin/env bash\nprintf 'k3d version v5.8.3 k3s1\\n'\n")
	writeShim(t, binDir, "kubectl", "#!/usr/bin/env bash\nfor a in \"$@\"; do if [ \"$a\" = \"get\" ]; then exit 1; fi; done\nexit 0\n")
	writeShim(t, binDir, "pg_dump", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "pg_restore", "#!/usr/bin/env bash\nexit 0\n")
	env = append(env, "CONFIRM=1")

	out, err := runDev(t, env, "reset-data")
	if err == nil {
		t.Fatalf("expected reset failure without postgres deployment:\n%s", out)
	}
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("expected exit 1, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "service_unhealthy") {
		t.Fatalf("expected service_unhealthy:\n%s", out)
	}
}

// TestCiProfileAutoPurgesOnExit covers the REQ-065 ci profile cleanup-timing
// contract (D4, AC-065-27): a FAILED dev-up auto-deletes the managed
// clusters and registry, while a SUCCESSFUL dev-up keeps the environment for
// REQ-066 consumption (teardown is the CI post-step dev-purge).
func TestCiProfileAutoPurgesOnExit(t *testing.T) {
	// Success path: environment retained.
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	env = append(env, "DEV_PROFILE=ci", "E2E_RUN_ID=ci-run-001", "DEV_JWT_SIGNING_KEY=ci-jwt-key")

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("ci dev-up failed:\n%s", out)
	}
	clustersPath := filepath.Join(stateDir, "clusters.txt")
	data, err := os.ReadFile(clustersPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) == "" {
		t.Fatal("expected clusters retained after successful ci dev-up (AC-065-27)")
	}

	// Failure path: a failing docker build aborts dev-up; the trap must
	// auto-purge the clusters and registry created so far.
	stateDir2 := t.TempDir()
	env2, binDir2 := fakeEnv(t, stateDir2)
	fakeK3d(t, binDir2, stateDir2)
	happyShims(t, binDir2)
	writeShim(t, binDir2, "docker",
		"#!/usr/bin/env bash\nfor a in \"$@\"; do if [ \"$a\" = \"inspect\" ]; then exit 1; fi; done\nif [ \"$1\" = \"build\" ]; then exit 1; fi\nexit 0\n")
	env2 = append(env2, "DEV_PROFILE=ci", "E2E_RUN_ID=ci-run-002", "DEV_JWT_SIGNING_KEY=ci-jwt-key")

	out, err := runDev(t, env2, "up")
	if err == nil {
		t.Fatalf("expected ci dev-up failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "docker_build_failed") {
		t.Fatalf("expected docker_build_failed:\n%s", out)
	}
	clustersPath2 := filepath.Join(stateDir2, "clusters.txt")
	data2, err := os.ReadFile(clustersPath2)
	if err == nil && strings.TrimSpace(string(data2)) != "" {
		t.Fatalf("expected all clusters purged after failed ci dev-up, remaining:\n%s", data2)
	}
}

// TestCleanCheckoutCreatesDataDirBeforeDiskGate reproduces the audit finding
// on AC-065-01: with a pristine checkout (data/ absent, gitignored and
// module-generated) the disk preflight must not fail. require_disk runs
// `df -Pk $DEV_DATA_DIR`, which emits nothing for a missing directory, so
// ownership_init (which mkdirs the data dir) must run BEFORE preflight_up.
// The awk shim forwards the real df output (empty when the target dir is
// missing), unlike happyShims which answers a fixed 500 GiB and would mask
// the ordering bug.
func TestCleanCheckoutCreatesDataDirBeforeDiskGate(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	// Simulate a pristine checkout: DEV_DATA_DIR does not exist yet.
	dataDir := filepath.Join(stateDir, "data")
	env = append(env, "DEV_DATA_DIR="+dataDir)
	fakeK3d(t, binDir, stateDir)
	writeShim(t, binDir, "docker", "#!/usr/bin/env bash\nfor a in \"$@\"; do if [ \"$a\" = \"inspect\" ]; then exit 1; fi; done\nexit 0\n")
	writeShim(t, binDir, "curl", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "kubectl", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "go", "#!/usr/bin/env bash\nif [ \"$1\" = \"env\" ]; then printf 'https://proxy.golang.org,direct\\n'; fi\nexit 0\n")
	writeShim(t, binDir, "nproc", "#!/usr/bin/env bash\nprintf '8\\n'\n")
	// awk: answer the memory probe with a pass, and make the disk probe
	// ordering-sensitive but filesystem-independent: a missing data dir
	// yields no `df -Pk` output and must fail the gate, while a present dir
	// answers a fixed 40 GiB (test temp dirs may live on a small tmpfs,
	// so forwarding the real number is environment-dependent).
	writeShim(t, binDir, "awk", `#!/usr/bin/env bash
if [[ "$*" == *"/proc/meminfo"* ]]; then
  printf '24576\n'
  exit 0
fi
count=0
while IFS= read -r line || [ -n "$line" ]; do
  count=$((count + 1))
  if [ "$count" -eq 2 ]; then
    printf '41943040\n'
    exit 0
  fi
done
exit 0
`)

	out, err := runDev(t, env, "up")
	if err != nil {
		t.Fatalf("dev-up on pristine checkout failed:\n%s", out)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("expected data dir created before preflight, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "dev-ownership.json")); err != nil {
		t.Fatalf("expected ownership manifest in data dir, got %v", err)
	}
}

// TestRegistryCreateUsesHostPortForm locks the k3d v5.8 registry port
// contract: `--port [HOST:]HOSTPORT` (the container port 5000 is fixed).
// The earlier three-part form "127.0.0.1:5001:5000" was rejected by real
// k3d ("Failed to parse registry port") while the fake shim accepted it
// silently — real-smoke regression found during the AC-065-01 audit repair.
func TestRegistryCreateUsesHostPortForm(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)

	out, err := runDev(t, env, "up")
	if err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}
	var registryCreate string
	for _, line := range k3dCreates(stateDir) {
		if strings.HasPrefix(line, "registry create ") {
			registryCreate = line
			break
		}
	}
	if registryCreate == "" {
		t.Fatalf("expected a registry create invocation, got: %v", k3dCreates(stateDir))
	}
	if !strings.Contains(registryCreate, "--port 127.0.0.1:5001") {
		t.Fatalf("expected [HOST:]HOSTPORT form --port 127.0.0.1:5001, got: %s", registryCreate)
	}
	if strings.Contains(registryCreate, ":5000") {
		t.Fatalf("container port must not be passed to k3d v5.8 (fixed at 5000): %s", registryCreate)
	}
}

// TestPurgeRemovesDataRuntimeFilesKeepsArchive covers AC-065-26: dev-purge
// deletes the data/ runtime credentials/keys/state files and keeps
// data/archive/. fakeEnv points DEV_DATA_DIR at the state dir itself, so the
// runtime files live directly under stateDir.
func TestPurgeRemovesDataRuntimeFilesKeepsArchive(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)

	files := []string{
		"dev-credentials.env", "dev-ownership.json", "dev-fixture.json",
		"dev-seed-progress.json", "dev-status.json", "kubeconfig.yaml",
		"backups/dump-20260824.sql",
		"dev-trust-root/dev-trust-root.key",
		"dev-jwt/jwt-signing-key.pem",
		"kubeconfigs/cluster.yaml",
	}
	for _, name := range files {
		path := filepath.Join(stateDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(stateDir, "archive", "archive-2026-08-14T10-00-00Z-v2.json")
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	env = append(env, "CONFIRM=1")
	out, err := runDev(t, env, "purge")
	if err != nil {
		t.Fatalf("purge failed:\n%s", out)
	}
	for _, name := range files {
		if _, err := os.Stat(filepath.Join(stateDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected %s purged, stat err=%v", name, err)
		}
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("data/archive must be preserved by dev-purge: %v", err)
	}
}

// TestJwtSigningKeyGeneratedAndReused covers D3: local dev-up generates a
// 0600 data/dev-jwt/jwt-signing-key.pem before apply, reuses it on re-runs,
// and rotation (delete + re-run) produces a fresh key.
func TestJwtSigningKeyGeneratedAndReused(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}
	// fakeEnv points DEV_DATA_DIR at the state dir: the key lives at
	// <stateDir>/dev-jwt/jwt-signing-key.pem.
	keyPath := filepath.Join(stateDir, "dev-jwt", "jwt-signing-key.pem")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("jwt signing key not generated: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 jwt signing key, got %o", info.Mode().Perm())
	}
	first, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("generated jwt key is empty")
	}

	// Re-run reuses the same key (no regeneration).
	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("second dev-up failed:\n%s", out)
	}
	second, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("jwt key regenerated on re-run; reuse contract violated")
	}

	// Rotation: delete the key and re-run — a fresh key appears.
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up after rotation failed:\n%s", out)
	}
	third, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, third) {
		t.Fatal("expected a fresh jwt key after rotation")
	}
}

// TestDevTimeoutReadyOverride covers AC-065-28 (readiness leg): when the
// endpoints never answer, dev-up reports service_unhealthy after the
// DEV_TIMEOUT_READY window (1s here) instead of the 300s default.
func TestDevTimeoutReadyOverride(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// curl fails only for the readiness probes; the registry readiness probe
	// still passes so the run reaches the readiness stage.
	writeShim(t, binDir, "curl", "#!/usr/bin/env bash\nif [[ \"$*\" == *\"/readyz\"* ]] || [[ \"$*\" == *\"8087\"* ]]; then exit 1; fi\nexit 0\n")
	env = append(env, "DEV_TIMEOUT_READY=1")

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("expected readiness failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "service_unhealthy") {
		t.Fatalf("expected service_unhealthy after DEV_TIMEOUT_READY expiry:\n%s", out)
	}
}

// TestDevSeedPassesTimeoutRetryOverrides covers AC-065-28 (seed legs): the
// DEV_TIMEOUT_OPERATOR / DEV_TIMEOUT_SEED_RETRIES overrides are forwarded to
// devseed as flags.
func TestDevSeedPassesTimeoutRetryOverrides(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	// dev-seed precondition: the JWT key must already exist (generated by a
	// prior dev-up, D3). fakeEnv points DEV_DATA_DIR at the state dir.
	keyPath := filepath.Join(stateDir, "dev-jwt", "jwt-signing-key.pem")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeShim(t, binDir, "flock", "#!/usr/bin/env bash\nexit 0\n")
	// The split-seed agents_up stage needs the enrollment tokens (written by
	// the fake devseed below), the docker IP probes and kubectl/kustomize
	// pass-throughs.
	writeShim(t, binDir, "go", "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \""+stateDir+"/go-calls.log\"\nif [ \"$1\" = \"env\" ]; then printf 'https://proxy.golang.org,direct\\n'; exit 0; fi\nmkdir -p \"$DEV_DATA_DIR/dev-enrollment-tokens\"\nfor c in dev-customer-a-direct dev-customer-a-cache dev-customer-b-replicated dev-customer-b-mixed; do printf 'fake-token\\n' > \"$DEV_DATA_DIR/dev-enrollment-tokens/$c.token\"; done\nexit 0\n")
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then
  if [ "$3" = "--format" ]; then
    case "${5}" in
      k3d-release-manager-control-server-0) printf '172.18.0.2\n'; exit 0 ;;
      k3d-release-manager-registry) printf '172.18.0.3\n'; exit 0 ;;
      *) exit 1 ;;
    esac
  fi
  exit 1
fi
exit 0
`)
	writeShim(t, binDir, "kubectl", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nexit 0\n")
	env = append(env, "DEV_TIMEOUT_OPERATOR=42", "DEV_TIMEOUT_SEED_RETRIES=7")

	out, err := runDev(t, env, "seed")
	if err != nil {
		t.Fatalf("seed failed:\n%s", out)
	}
	logged, err := os.ReadFile(filepath.Join(stateDir, "go-calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	all := string(logged)
	if !strings.Contains(all, "--operator-timeout 42") {
		t.Fatalf("expected --operator-timeout 42 forwarded to devseed:\n%s", all)
	}
	if !strings.Contains(all, "--seed-retries 7") {
		t.Fatalf("expected --seed-retries 7 forwarded to devseed:\n%s", all)
	}
}

// TestKustomizeBuildJwtSecretAndNoPostgresPVC covers AC-065-29 + D3 at the
// manifest level with the real kustomize: the dev overlay materializes the
// release-manager-jwt Secret from the local key file and no longer declares
// a PostgreSQL PVC. Skipped when kustomize is not on PATH.
func TestKustomizeBuildJwtSecretAndNoPostgresPVC(t *testing.T) {
	kustomize, err := exec.LookPath("kustomize")
	if err != nil {
		t.Skip("kustomize not installed")
	}
	root := repoRoot(t)
	keyPath := filepath.Join(root, "data", "dev-jwt", "jwt-signing-key.pem")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("test-jwt-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Join(root, "data", "dev-jwt")) //nolint:errcheck // best-effort test cleanup

	cmd := exec.CommandContext(context.Background(), kustomize, "build", "--load-restrictor", "LoadRestrictionsNone", "deploy/kustomize/dev")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kustomize build failed: %v\n%s", err, out)
	}
	manifest := string(out)
	// kustomize appends a content hash to the generated Secret name and
	// rewrites every reference: assert both the Secret resource and the
	// JWT_SIGNING_KEY data key.
	if !strings.Contains(manifest, "name: release-manager-jwt-") {
		t.Fatalf("expected generated release-manager-jwt-<hash> Secret in built manifest:\n%s", manifest)
	}
	if !strings.Contains(manifest, "key: JWT_SIGNING_KEY") {
		t.Fatalf("expected JWT_SIGNING_KEY data key in built manifest:\n%s", manifest)
	}
	if strings.Contains(manifest, "release-manager-postgres-data") {
		t.Fatalf("postgres PVC must be gone (AC-065-29):\n%s", manifest)
	}
	if !strings.Contains(manifest, "emptyDir: {}") {
		t.Fatalf("expected postgres emptyDir volume (AC-065-29):\n%s", manifest)
	}
}
