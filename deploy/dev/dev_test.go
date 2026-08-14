// Package devtest exercises the deploy/dev lifecycle module against fake
// CLIs. The tests assert error codes, exit codes, lock semantics and
// ownership gating through the module's public surface (REQ-065 AC-065-05/06/
// 07/11/14/20/21/22/23/25), without requiring Docker or k3d on the host.
package devtest

import (
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
	// registry container is only created by the fake k3d); other verbs
	// (start/rm) pass through.
	writeShim(t, binDir, "docker", "#!/usr/bin/env bash\nfor a in \"$@\"; do if [ \"$a\" = \"inspect\" ]; then exit 1; fi; done\nexit 0\n")
	writeShim(t, binDir, "curl", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "kubectl", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nexit 0\n")
	// go: answer the GOPROXY probe dev.sh forwards as a build-arg; the
	// seed path (go run ./cmd/devseed) just needs exit 0.
	writeShim(t, binDir, "go", "#!/usr/bin/env bash\nif [ \"$1\" = \"env\" ]; then printf 'https://proxy.golang.org,direct\\n'; fi\nexit 0\n")
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
	env = append(env, "HTTP_PROXY=http://127.0.0.1:7890", "HTTPS_PROXY=http://127.0.0.1:7890")

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

// TestCiProfileAutoPurgesOnExit covers the REQ-065 ci profile contract:
// dev-up exit with DEV_PROFILE=ci + E2E_RUN_ID set automatically deletes the
// managed clusters and registry (tear-down without an explicit dev-purge).
func TestCiProfileAutoPurgesOnExit(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	env = append(env, "DEV_PROFILE=ci", "E2E_RUN_ID=ci-run-001")

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("ci dev-up failed:\n%s", out)
	}
	// After the up trap, every cluster in the ownership manifest must have
	// been deleted: the fake-k3d clusters.txt file reflects current clusters.
	clustersPath := filepath.Join(stateDir, "clusters.txt")
	data, err := os.ReadFile(clustersPath)
	if err == nil && strings.TrimSpace(string(data)) != "" {
		t.Fatalf("expected all clusters purged after ci dev-up, remaining:\n%s", data)
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
	// awk: answer the memory probe with a pass, but forward the disk probe
	// output — a missing data dir yields no output and must fail the gate.
	writeShim(t, binDir, "awk", `#!/usr/bin/env bash
if [[ "$*" == *"/proc/meminfo"* ]]; then
  printf '24576\n'
  exit 0
fi
count=0
while IFS= read -r line || [ -n "$line" ]; do
  count=$((count + 1))
  if [ "$count" -eq 2 ]; then
    printf '%s\n' "$line" | tr -s ' ' | cut -d' ' -f4
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
