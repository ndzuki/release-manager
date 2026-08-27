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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// fakeOperatorGatewayPort is the loopback port fakeEnv points the operator
// mTLS gateway TCP readiness probe at (dev.sh DEV_OPERATOR_GATEWAY_PORT
// seam). It lives outside the DEV_PORTS_OVERRIDE band so the preflight port
// gate (require_ports_free) never sees it occupied.
const fakeOperatorGatewayPort = "19984"

var (
	fakeGatewayOnce sync.Once
	fakeGatewayLn   net.Listener
	fakeGatewayErr  error
)

// fakeOperatorGateway binds one shared loopback listener for the whole test
// package (fakeEnv is called multiple times per test; a per-call listener
// would collide with itself). The connection is accepted and immediately
// closed — the readiness probe only needs a TCP open to succeed.
func fakeOperatorGateway(t *testing.T) {
	t.Helper()
	fakeGatewayOnce.Do(func() {
		fakeGatewayLn, fakeGatewayErr = new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:"+fakeOperatorGatewayPort)
		if fakeGatewayErr != nil {
			return
		}
		go func() {
			for {
				conn, err := fakeGatewayLn.Accept()
				if err != nil {
					return
				}
				conn.Close()
			}
		}()
	})
	if fakeGatewayErr != nil {
		t.Fatalf("cannot bind fake operator gateway listener: %v", fakeGatewayErr)
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
		// The operator gateway readiness is a TCP probe without an HTTP
		// endpoint to shim; point dev.sh's probe at the shared loopback
		// listener above.
		"DEV_OPERATOR_GATEWAY_PORT="+fakeOperatorGatewayPort,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	fakeOperatorGateway(t)
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

// copyTree recursively copies a directory tree (used to isolate kustomize
// builds from the repository data dir).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copyTree %s -> %s: %v", src, dst, err)
	}
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
	// registry container is only created by dev.sh's registry_create) EXCEPT
	// for the --format IP probes used by agents_up (management node /
	// registry hostAliases); `container create` invocations are logged to
	// $DEV_DATA_DIR/docker-create.log so tests can assert the AC-065-32
	// label contract; other verbs (start/rm) pass through.
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "container" ] && [ "$2" = "create" ]; then
  printf '%s\n' "$*" >> "$DEV_DATA_DIR/docker-create.log"
  exit 0
fi
# AC-065-37: the CPU cap rides docker update --cpus (k3d has no CPU
# flag); record it so tests can assert defaults and overrides.
if [ "$1" = "update" ]; then
  printf '%s\n' "$*" >> "$DEV_DATA_DIR/docker-update.log"
  exit 0
fi
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
# AC-065-22 conflict gates probe network inspect; report the object absent
# (the fake env has no Docker networks) so creation proceeds.
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then exit 1; fi
exit 0
`)
	writeShim(t, binDir, "curl", `#!/usr/bin/env bash
# The fixture /version smoke (批次5 D10) must receive a version payload
# through the fake port-forward; every other probe just needs exit 0.
if [[ "$*" == *"/version"* ]]; then printf '{"version":"fixture-v2"}\n'; exit 0; fi
exit 0
`)
	writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
# The fixture /version smoke runs `+"`kubectl port-forward`"+` against the
# customer cluster; the fake forwards 127.0.0.1:18088 and stays alive until
# killed. The Redis readiness probe (批次5 D3) expects a PONG from
# `+"`kubectl exec ... redis-cli ping`"+`; the pg_isready probe only needs a
# zero exit. Everything else passes through.
for a in "$@"; do
  if [ "$a" = "port-forward" ]; then
    printf 'Forwarding from 127.0.0.1:18088 -> 8088\n'
    sleep 30
    exit 0
  fi
  if [ "$a" = "redis-cli" ]; then
    printf 'PONG\n'
    exit 0
  fi
done
exit 0
`)
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nexit 0\n")
	// go: answer the GOPROXY probe dev.sh forwards as a build-arg; the seed
	// path (go run ./cmd/devseed) writes the four enrollment tokens (the
	// split-seed agents_up stage consumes them) and exits 0. The mTLS CA
	// helper invocation (批次5 D1, AC-065-36) writes the two dummy CA
	// files and exits 0. -ensure-mtls-ca is a bool flag and the target dir
	// travels in the -mtls-ca-dir flag — the shim mirrors the REAL Go flag
	// semantics so a shell-side mismatch cannot be masked in tests.
	writeShim(t, binDir, "go", `#!/usr/bin/env bash
if [ "$1" = "env" ]; then printf 'https://proxy.golang.org,direct\n'; exit 0; fi
prev=""
for a in "$@"; do
  if [ "$prev" = "-mtls-ca-dir" ]; then
    mkdir -p "$a"
    printf 'fake-ca-key\n' > "$a/ca.key"
    printf 'fake-ca-cert\n' > "$a/ca.crt"
    chmod 600 "$a/ca.key" "$a/ca.crt"
    exit 0
  fi
  prev="$a"
done
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
	// AC-065-40 (批次5 D7): per-cluster and merged kubeconfigs are 0600.
	for _, path := range []string{
		filepath.Join(stateDir, "kubeconfig.yaml"),
		filepath.Join(stateDir, "kubeconfigs", "release-manager-control.yaml"),
		filepath.Join(stateDir, "kubeconfigs", "dev-customer-a-direct.yaml"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected %s, got %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("expected 0600 on %s, got %o", path, info.Mode().Perm())
		}
	}
	// AC-065-01 (批次5 D10): the fixture /version smoke runs through the
	// temporary port-forward and reports fixture-vN.
	if !strings.Contains(out, "fixture /version") || !strings.Contains(out, "fixture-v2") {
		t.Fatalf("expected fixture /version smoke output (fixture-vN):\n%s", out)
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
		"#!/usr/bin/env bash\nif [ \"$1\" = \"manifest\" ] && [ \"$2\" = \"inspect\" ]; then exit 1; fi\nif [ \"$1\" = \"container\" ] && [ \"$2\" = \"inspect\" ]; then exit 1; fi\nif [ \"$1\" = \"network\" ] && [ \"$2\" = \"inspect\" ]; then exit 1; fi\nprintf '%s\\n' \"$*\" >> \""+stateDir+"/docker-calls.log\"\nexit 0\n")
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
		// Real smoke 2026-08-27: k3d rejects unfiltered --env mappings on
		// multi-node clusters ("lacks a node filter, but there's more than
		// one node"); every proxy env must carry the @servers:* filter.
		if !strings.Contains(c, "@servers:*") {
			t.Fatalf("create proxy env missing @servers:* node filter: %s", c)
		}
		for _, envArg := range []string{"HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=k3d-release-manager-registry"} {
			if !strings.Contains(c, envArg) {
				t.Fatalf("create missing %s env injection: %s", envArg, c)
			}
		}
		// Real smoke 2026-08-27: NO_PROXY must cover the private CIDRs —
		// k3s honors HTTP(S)_PROXY for internal apiserver→kubelet and
		// pod↔pod HTTP traffic, so without them cluster-internal calls
		// route through the host proxy (values RPCs died, kubectl logs got
		// "proxyconnect tcp: proxy error ... 502").
		for _, cidr := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
			if !strings.Contains(c, cidr) {
				t.Fatalf("create NO_PROXY missing private CIDR %s: %s", cidr, c)
			}
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
			// Real smoke 2026-08-27: the google default module host is
			// unreachable directly from CN hosts and buildkit cannot reach a
			// loopback host proxy — the build chain must prepend the
			// directly-reachable goproxy.cn as the primary entry.
			!strings.Contains(line, "--build-arg GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct") {
			t.Fatalf("docker build missing proxy/GOPROXY build-args: %s", line)
		}
		// The first GOPROXY host must be exempted in NO_PROXY (module
		// fetches go direct instead of through the loopback proxy).
		if !strings.Contains(line, "--build-arg NO_PROXY=localhost,127.0.0.1,goproxy.cn") {
			t.Fatalf("docker build NO_PROXY missing goproxy host exemption: %s", line)
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
	env = append(env, "DEV_PROFILE=ci", "E2E_RUN_ID=ci-run-001", "DEV_JWT_SIGNING_KEY=ci-jwt-key", "DEV_WEBHOOK_SERVICE_TOKEN=ci-service-token",
		"DEV_M_TLS_CA_KEY=ci-ca-key", "DEV_M_TLS_CA_CERT=ci-ca-cert")

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
	env2 = append(env2, "DEV_PROFILE=ci", "E2E_RUN_ID=ci-run-002", "DEV_JWT_SIGNING_KEY=ci-jwt-key", "DEV_WEBHOOK_SERVICE_TOKEN=ci-service-token",
		"DEV_M_TLS_CA_KEY=ci-ca-key", "DEV_M_TLS_CA_CERT=ci-ca-cert")

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
	writeShim(t, binDir, "curl", `#!/usr/bin/env bash
if [[ "$*" == *"/version"* ]]; then printf '{"version":"fixture-v2"}\n'; exit 0; fi
exit 0
`)
	writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
for a in "$@"; do
  if [ "$a" = "port-forward" ]; then printf 'Forwarding from 127.0.0.1:18088 -> 8088\n'; sleep 30; exit 0; fi
  if [ "$a" = "redis-cli" ]; then printf 'PONG\n'; exit 0; fi
done
exit 0
`)
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "go", `#!/usr/bin/env bash
if [ "$1" = "env" ]; then printf 'https://proxy.golang.org,direct\n'; exit 0; fi
prev=""
for a in "$@"; do
  if [ "$prev" = "-mtls-ca-dir" ]; then
    mkdir -p "$a"
    printf 'fake-ca-key\n' > "$a/ca.key"
    printf 'fake-ca-cert\n' > "$a/ca.crt"
    chmod 600 "$a/ca.key" "$a/ca.crt"
    exit 0
  fi
  prev="$a"
done
mkdir -p "$DEV_DATA_DIR/dev-enrollment-tokens"
for c in dev-customer-a-direct dev-customer-a-cache dev-customer-b-replicated dev-customer-b-mixed; do
  printf 'fake-token\n' > "$DEV_DATA_DIR/dev-enrollment-tokens/$c.token"
done
exit 0
`)
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
// TestRegistryContainerCreatedWithManagedLabels covers AC-065-32: dev-up
// creates the registry container directly via Docker (k3d `registry create`
// has no label flag and Docker labels are immutable), carrying the k3d
// identification labels plus the managed/profile labels, the loopback port
// binding and the data volume.
func TestRegistryContainerCreatedWithManagedLabels(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)

	out, err := runDev(t, env, "up")
	if err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}
	logged, err := os.ReadFile(filepath.Join(stateDir, "docker-create.log"))
	if err != nil {
		t.Fatalf("docker container create not recorded: %v", err)
	}
	create := string(logged)
	if !strings.Contains(create, "--name k3d-release-manager-registry") {
		t.Fatalf("expected registry container create with explicit name:\n%s", create)
	}
	for _, label := range []string{
		"--label app=k3d",
		"--label k3d.role=registry",
		"--label io.release-manager.dev.managed=true",
		"--label io.release-manager.dev.profile=local",
	} {
		if !strings.Contains(create, label) {
			t.Fatalf("expected %s in docker container create:\n%s", label, create)
		}
	}
	if !strings.Contains(create, "--publish 127.0.0.1:5001:5000") {
		t.Fatalf("expected loopback port binding 127.0.0.1:5001:5000:\n%s", create)
	}
	if !strings.Contains(create, "--volume k3d-release-manager-registry:/var/lib/registry") {
		t.Fatalf("expected registry data volume mount:\n%s", create)
	}
	// No k3d registry create may remain: the container is dev-managed.
	for _, line := range k3dCreates(stateDir) {
		if strings.HasPrefix(line, "registry create ") {
			t.Fatalf("dev.sh must not call k3d registry create anymore:\n%s", line)
		}
	}
}

// TestUnlabeledSameNameResourceConflicts covers AC-065-22: a same-named
// Docker container / network WITHOUT the managed label and absent from the
// ownership whitelist stops dev-up with exit 1 + resource_conflict naming
// the conflicting object.
func TestUnlabeledSameNameResourceConflicts(t *testing.T) {
	t.Run("container", func(t *testing.T) {
		stateDir := t.TempDir()
		env, binDir := fakeEnv(t, stateDir)
		fakeK3d(t, binDir, stateDir)
		// The docker shim reports a same-named registry container that
		// exists but carries no managed label and is absent from the
		// ownership whitelist: the conflict gate must reject it instead of
		// adopting or relabeling it.
		writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then
  # Plain inspect: the object EXISTS. The --format label probe then prints
  # nothing (no managed label) and exits 1.
  if [ "$3" = "--format" ]; then exit 1; fi
  exit 0
fi
exit 0
`)
		writeShim(t, binDir, "curl", "#!/usr/bin/env bash\nexit 0\n")
		// Host-gate shims so the run reaches the registry stage.
		writeShim(t, binDir, "awk", "#!/usr/bin/env bash\ncat >/dev/null\nif [[ \"$*\" == *\"/proc/meminfo\"* ]]; then printf '24576\\n'; else printf '524288000\\n'; fi\n")
		writeShim(t, binDir, "df", "#!/usr/bin/env bash\nprintf 'Filesystem 1024-blocks Used Available Capacity Mounted on\\n/dev/x 1000000000 1 524288000 1%% /\\n'\n")
		writeShim(t, binDir, "nproc", "#!/usr/bin/env bash\nprintf '8\\n'\n")
		// The mTLS CA ensure (批次5 D1) runs before the registry stage; the
		// shim writes the dummy CA pair so the conflict gate is the failure.
		writeShim(t, binDir, "go", `#!/usr/bin/env bash
if [ "$1" = "env" ]; then printf 'https://proxy.golang.org,direct\n'; exit 0; fi
prev=""
for a in "$@"; do
  if [ "$prev" = "-mtls-ca-dir" ]; then
    mkdir -p "$a"
    printf 'fake-ca-key\n' > "$a/ca.key"
    printf 'fake-ca-cert\n' > "$a/ca.crt"
    chmod 600 "$a/ca.key" "$a/ca.crt"
    exit 0
  fi
  prev="$a"
done
exit 0
`)
		out, err := runDev(t, env, "up")
		if err == nil {
			t.Fatalf("expected resource_conflict, got success:\n%s", out)
		}
		if code := exitCode(t, err); code != 1 {
			t.Fatalf("expected exit 1, got %d\n%s", code, out)
		}
		if !strings.Contains(out, "resource_conflict") ||
			!strings.Contains(out, "k3d-release-manager-registry") {
			t.Fatalf("expected resource_conflict naming the container:\n%s", out)
		}
	})

	t.Run("network", func(t *testing.T) {
		stateDir := t.TempDir()
		env, binDir := fakeEnv(t, stateDir)
		fakeK3d(t, binDir, stateDir)
		happyShims(t, binDir)
		// The control cluster does not exist yet (create path), but a
		// foreign unlabeled k3d-release-manager-control network occupies the
		// name: the network conflict gate must fire before cluster create.
		writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then
  if [ "$3" = "--format" ]; then exit 1; fi
  exit 0
fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then exit 1; fi
exit 0
`)
		out, err := runDev(t, env, "up")
		if err == nil {
			t.Fatalf("expected resource_conflict, got success:\n%s", out)
		}
		if code := exitCode(t, err); code != 1 {
			t.Fatalf("expected exit 1, got %d\n%s", code, out)
		}
		if !strings.Contains(out, "resource_conflict") ||
			!strings.Contains(out, "k3d-release-manager-control") {
			t.Fatalf("expected resource_conflict naming the network:\n%s", out)
		}
	})
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
		"dev-service-tokens/webhook-service-token",
		"dev-ca/ca.key",
		"dev-ca/ca.crt",
		"diagnostics/20260825T120000Z/pods.txt",
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

// TestServiceTokenGeneratedAndReused covers 批次3 D2 / AC-065-33 (injection
// channel): local dev-up generates a 0600
// data/dev-service-tokens/webhook-service-token (32 chars [A-Za-z0-9]),
// reuses it on re-runs, and rotation (delete + re-run) produces a fresh one.
func TestServiceTokenGeneratedAndReused(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}
	tokenPath := filepath.Join(stateDir, "dev-service-tokens", "webhook-service-token")
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("webhook service token not generated: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 service token, got %o", info.Mode().Perm())
	}
	first, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(first))
	if len(token) != 32 || strings.Trim(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") != "" {
		t.Fatalf("expected 32-char [A-Za-z0-9] token, got %q", token)
	}

	// Re-run reuses the same token (no regeneration).
	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("second dev-up failed:\n%s", out)
	}
	second, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("service token regenerated on re-run; reuse contract violated")
	}

	// Rotation: delete the file and re-run — a fresh token appears.
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up after rotation failed:\n%s", out)
	}
	third, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, third) {
		t.Fatal("expected a fresh service token after rotation")
	}
}

// TestRegistryRelabelAdoptsWhitelistedLegacyContainer covers AC-065-32 / D8
// (relabel leg): a whitelisted legacy k3d-created registry without the
// managed label is recreated in place with the identity labels, preserving
// its image and /var/lib/registry volume (image cache).
func TestRegistryRelabelAdoptsWhitelistedLegacyContainer(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	// The legacy registry is already in the ownership whitelist from an
	// earlier dev.sh run.
	ownershipFile := filepath.Join(stateDir, "dev-ownership.json")
	if err := os.WriteFile(ownershipFile,
		[]byte(`{"profile":"local","created_at":"2026-08-24T00:00:00Z","fixture_version":"v2","k3d_clusters":[],"docker_containers":["k3d-release-manager-registry"],"docker_networks":[]}`+"\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	// docker shim: the legacy registry container exists without the managed
	// label (label probe fails), but exposes image/volume/state probes. Any
	// other container name reports absent so the cluster conflict gates pass.
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "container" ] && [ "$2" = "create" ]; then
  printf '%s\n' "$*" >> "$DEV_DATA_DIR/docker-create.log"
  exit 0
fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then
  if [ "$3" = "--format" ]; then
    name="$5"
    [ "$name" = "k3d-release-manager-registry" ] || exit 1
    case "${4}" in
      *'.Image'*) printf 'registry:3'; exit 0 ;;
      *'.Mounts'*) printf 'regvol'; exit 0 ;;
      *'.State.Running'*) printf 'true'; exit 0 ;;
      *'.Config.Labels'*) exit 1 ;;
      *) exit 1 ;;
    esac
  fi
  name="$3"
  [ "$name" = "k3d-release-manager-registry" ] || exit 1
  exit 0
fi
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then exit 1; fi
exit 0
`)
	writeShim(t, binDir, "curl", `#!/usr/bin/env bash
if [[ "$*" == *"/version"* ]]; then printf '{"version":"fixture-v2"}\n'; exit 0; fi
exit 0
`)
	writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
for a in "$@"; do
  if [ "$a" = "port-forward" ]; then printf 'Forwarding from 127.0.0.1:18088 -> 8088\n'; sleep 30; exit 0; fi
  if [ "$a" = "redis-cli" ]; then printf 'PONG\n'; exit 0; fi
done
exit 0
`)
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "go", `#!/usr/bin/env bash
if [ "$1" = "env" ]; then printf 'https://proxy.golang.org,direct\n'; exit 0; fi
prev=""
for a in "$@"; do
  if [ "$prev" = "-mtls-ca-dir" ]; then
    mkdir -p "$a"
    printf 'fake-ca-key\n' > "$a/ca.key"
    printf 'fake-ca-cert\n' > "$a/ca.crt"
    chmod 600 "$a/ca.key" "$a/ca.crt"
    exit 0
  fi
  prev="$a"
done
mkdir -p "$DEV_DATA_DIR/dev-enrollment-tokens"
for c in dev-customer-a-direct dev-customer-a-cache dev-customer-b-replicated dev-customer-b-mixed; do
  printf 'fake-token\n' > "$DEV_DATA_DIR/dev-enrollment-tokens/$c.token"
done
exit 0
`)
	writeShim(t, binDir, "awk", "#!/usr/bin/env bash\ncat >/dev/null\nif [[ \"$*\" == *\"/proc/meminfo\"* ]]; then printf '24576\\n'; else printf '524288000\\n'; fi\n")
	writeShim(t, binDir, "df", "#!/usr/bin/env bash\nprintf 'Filesystem 1024-blocks Used Available Capacity Mounted on\\n/dev/x 1000000000 1 524288000 1%% /\\n'\n")
	writeShim(t, binDir, "nproc", "#!/usr/bin/env bash\nprintf '8\\n'\n")

	out, err := runDev(t, env, "up")
	if err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}
	logged, err := os.ReadFile(filepath.Join(stateDir, "docker-create.log"))
	if err != nil {
		t.Fatalf("docker container create not recorded: %v", err)
	}
	create := string(logged)
	if !strings.Contains(create, "--label io.release-manager.dev.managed=true") ||
		!strings.Contains(create, "--label io.release-manager.dev.profile=local") {
		t.Fatalf("relabel must recreate the container with managed/profile labels:\n%s", create)
	}
	if !strings.Contains(create, "--volume regvol:/var/lib/registry") {
		t.Fatalf("relabel must preserve the registry data volume:\n%s", create)
	}
	if !strings.HasSuffix(strings.TrimSpace(create), "registry:3") {
		t.Fatalf("relabel must reuse the existing registry image:\n%s", create)
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
	// dev-seed precondition: the JWT key, the webhook service token and the
	// dev mTLS CA must already exist (generated by a prior dev-up, D3 /
	// 批次3 D2 / 批次5 D1). fakeEnv points DEV_DATA_DIR at the state dir.
	keyPath := filepath.Join(stateDir, "dev-jwt", "jwt-signing-key.pem")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(stateDir, "dev-service-tokens", "webhook-service-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("service-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	caKeyPath := filepath.Join(stateDir, "dev-ca", "ca.key")
	if err := os.MkdirAll(filepath.Dir(caKeyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caKeyPath, []byte("ca-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "dev-ca", "ca.crt"), []byte("ca-cert"), 0o600); err != nil {
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
	writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
for a in "$@"; do
  if [ "$a" = "port-forward" ]; then printf 'Forwarding from 127.0.0.1:18088 -> 8088\n'; sleep 30; exit 0; fi
  if [ "$a" = "redis-cli" ]; then printf 'PONG\n'; exit 0; fi
done
exit 0
`)
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "curl", `#!/usr/bin/env bash
if [[ "$*" == *"/version"* ]]; then printf '{"version":"fixture-v2"}\n'; exit 0; fi
exit 0
`)
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
//
// The build runs in an isolated temp tree (symlinked deploy/kustomize +
// temp data/dev-jwt) — an earlier version wrote the key into the REAL repo
// data dir and RemoveAll'ed it on cleanup, which wiped a concurrently
// running dev environment's JWT key (real-smoke regression 2026-08-24:
// kustomize_build_failed mid-run).
func TestKustomizeBuildJwtSecretAndNoPostgresPVC(t *testing.T) {
	kustomize, err := exec.LookPath("kustomize")
	if err != nil {
		t.Skip("kustomize not installed")
	}
	root := repoRoot(t)
	tmp := t.TempDir()
	// Mirror the repo-relative layout the overlay's secretGenerator expects:
	// ../../../data/dev-jwt/jwt-signing-key.pem relative to
	// deploy/kustomize/dev resolves inside tmp. A symlink does NOT work:
	// kustomize resolves the kustomization dir through symlinks and then
	// resolves relative file sources from the REAL location — the key path
	// would land back in the repo data dir (the destructive bug this test
	// isolation fixes). Copy the tree instead.
	copyTree(t, filepath.Join(root, "deploy", "kustomize"), filepath.Join(tmp, "deploy", "kustomize"))
	keyPath := filepath.Join(tmp, "data", "dev-jwt", "jwt-signing-key.pem")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("test-jwt-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	// AC-065-33: the webhook-service-token secretGenerator sources the same
	// data-dir layout; provide the file in the isolated tree.
	tokenPath := filepath.Join(tmp, "data", "dev-service-tokens", "webhook-service-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("test-service-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	// AC-065-36: the mTLS CA secretGenerator sources data/dev-ca/ (ca.key +
	// ca.crt) generated by dev-up before apply.
	caDir := filepath.Join(tmp, "data", "dev-ca")
	if err := os.MkdirAll(caDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.key"), []byte("test-ca-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.crt"), []byte("test-ca-cert"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(context.Background(), kustomize, "build", "--load-restrictor", "LoadRestrictionsNone", "deploy/kustomize/dev")
	cmd.Dir = tmp
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
	// AC-065-33: the webhook-service-token Secret is generated with a content
	// hash and wired into the webhook (sender) and orchestrator (verifier)
	// Deployments.
	if !strings.Contains(manifest, "name: release-manager-webhook-service-token-") {
		t.Fatalf("expected generated release-manager-webhook-service-token-<hash> Secret in built manifest:\n%s", manifest)
	}
	if !strings.Contains(manifest, "key: WEBHOOK_SERVICE_TOKEN") {
		t.Fatalf("expected WEBHOOK_SERVICE_TOKEN data key in built manifest:\n%s", manifest)
	}
	if !strings.Contains(manifest, "name: DEV_WEBHOOK_SERVICE_TOKEN") {
		t.Fatalf("expected DEV_WEBHOOK_SERVICE_TOKEN env wiring in built manifest:\n%s", manifest)
	}
	if !strings.Contains(manifest, "name: DEV_WEBHOOK_SERVICE_TOKEN_PREVIOUS") {
		t.Fatalf("expected DEV_WEBHOOK_SERVICE_TOKEN_PREVIOUS env wiring (rotation seam) in built manifest:\n%s", manifest)
	}
	if !strings.Contains(manifest, "optional: true") {
		t.Fatalf("expected optional previous-token key (zero-downtime rotation):\n%s", manifest)
	}
	// AC-065-36: the mTLS CA Secret is generated with a content hash and
	// mounted into the orchestrator gateway paths (/data/gateway-ca.key +
	// /data/gateway-ca.crt, the ca.key_path/ca.cert_path config targets).
	if !strings.Contains(manifest, "name: release-manager-mtls-ca-") {
		t.Fatalf("expected generated release-manager-mtls-ca-<hash> Secret in built manifest:\n%s", manifest)
	}
	if !strings.Contains(manifest, "\n  ca.key: ") || !strings.Contains(manifest, "\n  ca.crt: ") {
		t.Fatalf("expected ca.key/ca.crt data keys in the mtls-ca Secret:\n%s", manifest)
	}
	if !strings.Contains(manifest, "mountPath: /data/gateway-ca.key") ||
		!strings.Contains(manifest, "mountPath: /data/gateway-ca.crt") {
		t.Fatalf("expected gateway CA key/cert mounts on the orchestrator Deployment:\n%s", manifest)
	}
	if !strings.Contains(manifest, "subPath: ca.key") || !strings.Contains(manifest, "subPath: ca.crt") {
		t.Fatalf("expected gateway CA subPath mounts on the orchestrator Deployment:\n%s", manifest)
	}
}

// TestMtlsCaGeneratedAndReused covers 批次5 D1 / AC-065-36 (local leg):
// dev-up delegates the dev mTLS CA ensure to the devseed helper
// (go run -ensure-mtls-ca -mtls-ca-dir <dir>) before any deployment stage, and the pair
// lands as 0600 files in a 0700 dir. The helper owns reuse vs regeneration
// (Go-tested); the shell contract asserted here is the invocation, the file
// presence and the permissions, plus regeneration after rotation.
func TestMtlsCaGeneratedAndReused(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// Replace the go shim with one that logs every -mtls-ca-dir value and
	// generates non-deterministic content (rotation must be observable).
	// The shim parses -mtls-ca-dir as a value-taking flag, matching the real
	// Go flag semantics of cmd/devseed.
	writeShim(t, binDir, "go", `#!/usr/bin/env bash
if [ "$1" = "env" ]; then printf 'https://proxy.golang.org,direct\n'; exit 0; fi
printf '%s\n' "$*" >> "$DEV_DATA_DIR/go-calls.log"
prev=""
for a in "$@"; do
  if [ "$prev" = "-mtls-ca-dir" ]; then
    n=$(cat "$DEV_DATA_DIR/ca-count" 2>/dev/null || printf 0)
    n=$((n + 1))
    printf '%s' "$n" > "$DEV_DATA_DIR/ca-count"
    mkdir -p "$a"
    printf 'fake-ca-key-%s\n' "$n" > "$a/ca.key"
    printf 'fake-ca-cert-%s\n' "$n" > "$a/ca.crt"
    chmod 600 "$a/ca.key" "$a/ca.crt"
    exit 0
  fi
  prev="$a"
done
mkdir -p "$DEV_DATA_DIR/dev-enrollment-tokens"
for c in dev-customer-a-direct dev-customer-a-cache dev-customer-b-replicated dev-customer-b-mixed; do
  printf 'fake-token\n' > "$DEV_DATA_DIR/dev-enrollment-tokens/$c.token"
done
exit 0
`)

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}
	caDir := filepath.Join(stateDir, "dev-ca")
	keyPath := filepath.Join(caDir, "ca.key")
	certPath := filepath.Join(caDir, "ca.crt")
	for _, path := range []string{keyPath, certPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected %s, got %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("expected 0600 on %s, got %o", path, info.Mode().Perm())
		}
	}
	dirInfo, err := os.Stat(caDir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("expected 0700 dev-ca dir, got %o", dirInfo.Mode().Perm())
	}
	// The helper invocation targeted the data/dev-ca dir.
	logged, err := os.ReadFile(filepath.Join(stateDir, "go-calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "-ensure-mtls-ca -mtls-ca-dir "+caDir) {
		t.Fatalf("expected -ensure-mtls-ca -mtls-ca-dir %s delegation:\n%s", caDir, logged)
	}
	// Re-run delegates again (reuse vs regenerate is the helper's own
	// contract, locked by cmd/devseed TestEnsureDevMTLSCA_GenerateReuse...);
	// the shell contract is that the delegation happens on every run and
	// the pair remains present with the right permissions.
	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("second dev-up failed:\n%s", out)
	}
	logged, err = os.ReadFile(filepath.Join(stateDir, "go-calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(logged), "-ensure-mtls-ca -mtls-ca-dir "+caDir); got != 2 {
		t.Fatalf("expected the CA helper delegated on both runs, got %d:\n%s", got, logged)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("dev mTLS CA key missing after re-run: %v", err)
	}
	// Rotation: delete the key and re-run — the helper regenerates.
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up after rotation failed:\n%s", out)
	}
	recreated, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("expected a recreated dev mTLS CA key after rotation: %v", err)
	}
	if recreated.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 recreated key, got %o", recreated.Mode().Perm())
	}
}

// TestCiProfileMtlsCaTransientFilesRemoved covers 批次5 D1 (ci leg,
// AC-065-36): the ci profile materializes DEV_M_TLS_CA_KEY/CERT into the
// kustomize source path transiently and never leaves them on disk after a
// successful dev-up.
func TestCiProfileMtlsCaTransientFilesRemoved(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	env = append(env, "DEV_PROFILE=ci", "E2E_RUN_ID=ci-run-003", "DEV_JWT_SIGNING_KEY=ci-jwt-key",
		"DEV_WEBHOOK_SERVICE_TOKEN=ci-service-token", "DEV_M_TLS_CA_KEY=ci-ca-key", "DEV_M_TLS_CA_CERT=ci-ca-cert")

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("ci dev-up failed:\n%s", out)
	}
	for _, path := range []string{
		filepath.Join(stateDir, "dev-ca", "ca.key"),
		filepath.Join(stateDir, "dev-ca", "ca.crt"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ci profile must not persist the transient mTLS CA file %s (stat err=%v)", path, err)
		}
	}
}

// TestDevDownTeardownOrderAndKubeconfigCleanup covers ② D-017 / AC-065-04:
// dev-down deletes the clusters, then cleans their kubeconfigs + ownership
// entries, disconnects the registry from each cluster network BEFORE
// removing the network (real smoke: a network with the registry attached
// cannot be removed), and drops the merged kubeconfig when no cluster
// remains.
func TestDevDownTeardownOrderAndKubeconfigCleanup(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}
	for _, cluster := range []string{
		"release-manager-control", "dev-customer-a-direct", "dev-customer-a-cache",
		"dev-customer-b-replicated", "dev-customer-b-mixed",
	} {
		if _, err := os.Stat(filepath.Join(stateDir, "kubeconfigs", cluster+".yaml")); err != nil {
			t.Fatalf("expected kubeconfig for %s after dev-up, got %v", cluster, err)
		}
	}
	// Swap in a teardown-observing docker shim: networks and the registry
	// container exist, and every network verb is logged in argument order.
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "network" ]; then
  printf '%s\n' "$*" >> "$DEV_DATA_DIR/docker-net.log"
  exit 0
fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then exit 0; fi
exit 0
`)

	if out, err := runDev(t, env, "down"); err != nil {
		t.Fatalf("dev-down failed:\n%s", out)
	}
	// Kubeconfigs for the deleted clusters are gone; nothing remains to
	// merge, so the merged file is deleted too (AC-065-04).
	for _, cluster := range []string{
		"release-manager-control", "dev-customer-a-direct", "dev-customer-a-cache",
		"dev-customer-b-replicated", "dev-customer-b-mixed",
	} {
		if _, err := os.Stat(filepath.Join(stateDir, "kubeconfigs", cluster+".yaml")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected kubeconfig for %s removed by dev-down (stat err=%v)", cluster, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "kubeconfig.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected merged kubeconfig removed when no cluster remains (stat err=%v)", err)
	}
	// Ownership: clusters and networks removed; registry entry retained.
	manifest, err := os.ReadFile(filepath.Join(stateDir, "dev-ownership.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `"k3d_clusters":[]`) {
		t.Fatalf("expected empty k3d_clusters after dev-down:\n%s", manifest)
	}
	if !strings.Contains(string(manifest), `"docker_networks":[]`) {
		t.Fatalf("expected empty docker_networks after dev-down:\n%s", manifest)
	}
	if !strings.Contains(string(manifest), "k3d-release-manager-registry") {
		t.Fatalf("dev-down must retain the registry ownership entry:\n%s", manifest)
	}
	// D-017 order: for every cluster network the registry disconnect line
	// precedes the network removal line.
	logged, err := os.ReadFile(filepath.Join(stateDir, "docker-net.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, network := range []string{
		"k3d-release-manager-control", "k3d-dev-customer-a-direct", "k3d-dev-customer-a-cache",
		"k3d-dev-customer-b-replicated", "k3d-dev-customer-b-mixed",
	} {
		disconnectIdx := bytes.Index(logged, []byte("disconnect -f "+network))
		rmIdx := bytes.Index(logged, []byte("rm "+network))
		if disconnectIdx == -1 {
			t.Fatalf("expected registry disconnect for %s:\n%s", network, logged)
		}
		if rmIdx == -1 {
			t.Fatalf("expected network removal for %s:\n%s", network, logged)
		}
		if disconnectIdx > rmIdx {
			t.Fatalf("D-017 order violated for %s: disconnect must precede rm:\n%s", network, logged)
		}
	}
}

// TestK3dNodeResourceDefaultsAndOverride covers AC-065-37 (批次5 D2):
// k3d create receives --servers-memory with the deterministic class defaults
// (control 3GiB, customers 1.5GiB) and the CPU cap is applied via docker
// update (control 2, customers 1); DEV_K3D_NODE_MEMORY / DEV_K3D_NODE_CPU
// override both classes.
func TestK3dNodeResourceDefaultsAndOverride(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		stateDir := t.TempDir()
		env, binDir := fakeEnv(t, stateDir)
		fakeK3d(t, binDir, stateDir)
		happyShims(t, binDir)
		if out, err := runDev(t, env, "up"); err != nil {
			t.Fatalf("dev-up failed:\n%s", out)
		}
		creates := clusterCreates(k3dCreates(stateDir))
		if len(creates) != 5 {
			t.Fatalf("expected 5 creates, got %d", len(creates))
		}
		for _, c := range creates {
			if strings.HasPrefix(c, "release-manager-control|") && !strings.Contains(c, "--servers-memory 3GiB") {
				t.Fatalf("control cluster missing default 3GiB:\n%s", c)
			}
			if !strings.HasPrefix(c, "release-manager-control|") && !strings.Contains(c, "--servers-memory 1.5GiB") {
				t.Fatalf("customer cluster missing default 1.5GiB:\n%s", c)
			}
		}
		updates, err := os.ReadFile(filepath.Join(stateDir, "docker-update.log"))
		if err != nil {
			t.Fatalf("docker update not recorded: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(updates)), "\n")
		if len(lines) != 5 {
			t.Fatalf("expected 5 docker update calls, got %d:\n%s", len(lines), updates)
		}
		// AC-065-37: both caps ride docker update (k3d has no CPU flag; the
		// memory cap is re-applied on resume too, keeping overrides effective).
		for _, line := range lines {
			if strings.Contains(line, "k3d-release-manager-control-server-0") &&
				(!strings.Contains(line, "--cpus 2") || !strings.Contains(line, "--memory 3GiB")) {
				t.Fatalf("expected control cluster caps (--cpus 2 --memory 3GiB):\n%s", line)
			}
			if !strings.Contains(line, "k3d-release-manager-control-server-0") &&
				(!strings.Contains(line, "--cpus 1") || !strings.Contains(line, "--memory 1.5GiB")) {
				t.Fatalf("expected customer cluster caps (--cpus 1 --memory 1.5GiB):\n%s", line)
			}
		}
	})
	t.Run("override", func(t *testing.T) {
		stateDir := t.TempDir()
		env, binDir := fakeEnv(t, stateDir)
		fakeK3d(t, binDir, stateDir)
		happyShims(t, binDir)
		env = append(env, "DEV_K3D_NODE_MEMORY=2GiB", "DEV_K3D_NODE_CPU=4")
		if out, err := runDev(t, env, "up"); err != nil {
			t.Fatalf("dev-up failed:\n%s", out)
		}
		creates := clusterCreates(k3dCreates(stateDir))
		for _, c := range creates {
			if !strings.Contains(c, "--servers-memory 2GiB") {
				t.Fatalf("override memory missing:\n%s", c)
			}
		}
		updates, err := os.ReadFile(filepath.Join(stateDir, "docker-update.log"))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(updates)), "\n") {
			if !strings.Contains(line, "--cpus 4") || !strings.Contains(line, "--memory 2GiB") {
				t.Fatalf("override caps missing in docker update:\n%s", line)
			}
		}
	})
}

// TestBuildParallelismSequentialParallelInvalid covers AC-065-38 (批次5 D5):
// the default is sequential builds (no overlap), DEV_BUILD_PARALLELISM=2
// overlaps up to 2 builds, and an invalid value falls back to sequential.
func TestBuildParallelismSequentialParallelInvalid(t *testing.T) {
	buildOrder := func(t *testing.T, env []string) []byte {
		t.Helper()
		stateDir := t.TempDir()
		env2, binDir := fakeEnv(t, stateDir)
		fakeK3d(t, binDir, stateDir)
		happyShims(t, binDir)
		writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "manifest" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "build" ]; then printf 'B\n' >> "$DEV_DATA_DIR/build-order.log"; sleep 0.5; printf 'E\n' >> "$DEV_DATA_DIR/build-order.log"; exit 0; fi
if [ "$1" = "push" ]; then exit 0; fi
exit 0
`)
		env2 = append(env2, env...)
		if out, err := runDev(t, env2, "up"); err != nil {
			t.Fatalf("dev-up failed:\n%s", out)
		}
		logged, err := os.ReadFile(filepath.Join(stateDir, "build-order.log"))
		if err != nil {
			t.Fatal(err)
		}
		return logged
	}
	marker := func(t *testing.T, env []string) string {
		t.Helper()
		return strings.TrimSpace(string(buildOrder(t, env)))
	}

	t.Run("sequential default", func(t *testing.T) {
		order := marker(t, nil)
		if strings.Contains(order, "B\nB") {
			t.Fatalf("default builds must be sequential, got overlap:\n%s", order)
		}
		if got := strings.Count(order, "B"); got != 8 {
			t.Fatalf("expected 8 builds, got %d:\n%s", got, order)
		}
	})
	t.Run("parallelism 2", func(t *testing.T) {
		order := marker(t, []string{"DEV_BUILD_PARALLELISM=2"})
		if !strings.Contains(order, "B\nB") {
			t.Fatalf("parallelism 2 must overlap builds, got:\n%s", order)
		}
		if strings.Contains(order, "B\nB\nB") {
			t.Fatalf("parallelism 2 must cap concurrency at 2, got:\n%s", order)
		}
		if got := strings.Count(order, "B"); got != 8 {
			t.Fatalf("expected 8 builds, got %d:\n%s", got, order)
		}
	})
	t.Run("invalid falls back sequential", func(t *testing.T) {
		order := marker(t, []string{"DEV_BUILD_PARALLELISM=3"})
		if strings.Contains(order, "B\nB") {
			t.Fatalf("invalid parallelism must fall back to sequential, got overlap:\n%s", order)
		}
	})
}

// TestBuildParallelismMixedFailureReportsFirstJobCode covers the parallel
// scheduler error mapping (Spec 轴审查发现): when the FIRST failed job is a
// build failure (10) and a later job fails with push (11), dev-up must
// report docker_build_failed naming the first service — the old code
// recorded only the LAST failure's rc and misreported docker_push_failed.
func TestBuildParallelismMixedFailureReportsFirstJobCode(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "manifest" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "build" ]; then
  # The FIRST service (webhook) fails the build; every later service fails
  # the push — the report must follow the first failure's class.
  if [[ "$*" == *"release-webhook"* ]]; then printf 'B\n' >> "$DEV_DATA_DIR/build-order.log"; exit 1; fi
  exit 0
fi
if [ "$1" = "push" ]; then
  # Only release-* image pushes fail (k3s component prewarm pushes pass).
  if [[ "$*" == *"release-"* ]]; then
    printf 'P\n' >> "$DEV_DATA_DIR/build-order.log"
    exit 1
  fi
  exit 0
fi
exit 0
`)
	env = append(env, "DEV_BUILD_PARALLELISM=2")

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("expected build failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "docker_build_failed") {
		t.Fatalf("expected docker_build_failed (first failure class):\n%s", out)
	}
	if !strings.Contains(out, "release-webhook") {
		t.Fatalf("expected the first failed service named:\n%s", out)
	}
	if strings.Contains(out, "docker_push_failed") {
		t.Fatalf("a later push failure must not override the first build failure class:\n%s", out)
	}
}

// TestKubectlExecDbReadiness covers 批次5 D3 (AC-065-01): PostgreSQL and
// Redis readiness are probed with kubectl exec into the pinned image pods
// (pg_isready / redis-cli ping), and a never-ready PostgreSQL fails
// dev-up with service_unhealthy after DEV_TIMEOUT_READY.
func TestKubectlExecDbReadiness(t *testing.T) {
	t.Run("probes recorded", func(t *testing.T) {
		stateDir := t.TempDir()
		env, binDir := fakeEnv(t, stateDir)
		fakeK3d(t, binDir, stateDir)
		happyShims(t, binDir)
		writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$DEV_DATA_DIR/kubectl.log"
for a in "$@"; do
  if [ "$a" = "port-forward" ]; then printf 'Forwarding from 127.0.0.1:18088 -> 8088\n'; sleep 30; exit 0; fi
  if [ "$a" = "redis-cli" ]; then printf 'PONG\n'; exit 0; fi
done
exit 0
`)
		if out, err := runDev(t, env, "up"); err != nil {
			t.Fatalf("dev-up failed:\n%s", out)
		}
		logged, err := os.ReadFile(filepath.Join(stateDir, "kubectl.log"))
		if err != nil {
			t.Fatal(err)
		}
		all := string(logged)
		if !strings.Contains(all, "exec deployment/postgres -- pg_isready") {
			t.Fatalf("expected kubectl exec pg_isready probe:\n%s", all)
		}
		if !strings.Contains(all, "exec deployment/redis -- redis-cli ping") {
			t.Fatalf("expected kubectl exec redis-cli ping probe:\n%s", all)
		}
	})
	t.Run("postgres never ready", func(t *testing.T) {
		stateDir := t.TempDir()
		env, binDir := fakeEnv(t, stateDir)
		fakeK3d(t, binDir, stateDir)
		happyShims(t, binDir)
		// Only the pg_isready probe fails; apply/port-forward/redis must
		// keep passing so the run reaches the readiness stage (a blanket
		// exit 1 would fail kubectl apply first and mask the probe).
		writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
if [[ "$*" == *"pg_isready"* ]]; then exit 1; fi
for a in "$@"; do
  if [ "$a" = "port-forward" ]; then printf 'Forwarding from 127.0.0.1:18088 -> 8088\n'; sleep 30; exit 0; fi
  if [ "$a" = "redis-cli" ]; then printf 'PONG\n'; exit 0; fi
done
exit 0
`)
		env = append(env, "DEV_TIMEOUT_READY=1")
		out, err := runDev(t, env, "up")
		if err == nil {
			t.Fatalf("expected readiness failure, got success:\n%s", out)
		}
		if !strings.Contains(out, "service_unhealthy") || !strings.Contains(out, "pg_isready") {
			t.Fatalf("expected service_unhealthy naming the pg_isready probe:\n%s", out)
		}
	})
}

// TestDiagnosticsCollectedOnFailureAndPurged covers 批次5 D6 / AC-065-39:
// a failed dev-up collects kubectl describe/get/logs into
// data/diagnostics/<ISO8601>/ (0600 files, one-line stderr summary), and
// dev-purge removes the directory.
func TestDiagnosticsCollectedOnFailureAndPurged(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// The build fails after the clusters were created, so the merged
	// kubeconfig exists and diagnostics have something to inspect.
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "manifest" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "build" ]; then exit 1; fi
exit 0
`)

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("expected docker_build_failed, got success:\n%s", out)
	}
	if !strings.Contains(out, "docker_build_failed") {
		t.Fatalf("expected docker_build_failed:\n%s", out)
	}
	diagDir := filepath.Join(stateDir, "diagnostics")
	entries, err := os.ReadDir(diagDir)
	if err != nil {
		t.Fatalf("expected diagnostics dir after failure: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one ISO8601 diagnostics snapshot, got %d", len(entries))
	}
	snapshot := filepath.Join(diagDir, entries[0].Name())
	for _, name := range []string{"pods.txt", "resources.txt", "events.txt", "describe-pods.txt"} {
		info, err := os.Stat(filepath.Join(snapshot, name))
		if err != nil {
			t.Fatalf("expected collected %s, got %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("expected 0600 on %s, got %o", name, info.Mode().Perm())
		}
	}

	// dev-purge removes the diagnostics directory (AC-065-39 second leg).
	env = append(env, "CONFIRM=1")
	if out, err := runDev(t, env, "purge"); err != nil {
		t.Fatalf("purge failed:\n%s", out)
	}
	if _, err := os.Stat(diagDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected diagnostics dir purged (stat err=%v)", err)
	}
}

// TestFixtureVersionSmokeRejectsUnexpectedPayload covers 批次5 D10 negative
// leg (AC-065-01): a /version payload outside fixture-vN fails dev-up with
// service_unhealthy naming the unexpected payload.
func TestFixtureVersionSmokeRejectsUnexpectedPayload(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	writeShim(t, binDir, "curl", `#!/usr/bin/env bash
if [[ "$*" == *"/version"* ]]; then printf '{"version":"production-image"}\n'; exit 0; fi
exit 0
`)

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("expected fixture /version smoke failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "service_unhealthy") || !strings.Contains(out, "unexpected payload") {
		t.Fatalf("expected service_unhealthy with unexpected payload:\n%s", out)
	}
}
