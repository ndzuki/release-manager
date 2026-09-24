// Package devtest exercises the deploy/dev lifecycle module against fake
// CLIs. The tests assert error codes, exit codes, lock semantics and
// ownership gating through the module's public surface (REQ-065 AC-065-05/06/
// 07/11/14/20/21/22/23/25), without requiring Docker or k3d on the host.
package devtest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	e2e "github.com/ndzuki/release-manager/test/e2e"

	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/jwtauth"
	"gopkg.in/yaml.v3"
)

// testJWTPrivateKeyPEM mints a PKCS#8 Ed25519 private key for the tests that
// must hand the lifecycle module real key material (REQ-065 AC-065-01: the
// helper validates through the same parser cmd/auth uses, so a placeholder
// string no longer configures the ci profile).
func testJWTPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate test JWT key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal test JWT key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// writeDevJWTKeyFiles plants both halves of the pair where dev-up expects them.
// The pre-existing-key tests only need the files to exist; the kustomize test
// feeds them to secretGenerator.
func writeDevJWTKeyFiles(t *testing.T, dir, privatePEM, publicPEM string) {
	t.Helper()
	if publicPEM == "" {
		// The seed precondition checks that BOTH halves exist and are non-empty,
		// so derive the public half rather than planting an empty file.
		publicPEM = testPublicKeyPEM(t, privatePEM)
	}
	keyDir := filepath.Join(dir, "dev-jwt")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "jwt-private-key.pem"), []byte(privatePEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "jwt-public-key.pem"), []byte(publicPEM), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testPublicKeyPEM derives the public half of a PKCS#8 private key PEM.
func testPublicKeyPEM(t *testing.T, privatePEM string) string {
	t.Helper()
	privateKey, err := jwtauth.ParseEd25519PrivateKeyPEM(privatePEM)
	if err != nil {
		t.Fatalf("parse test JWT private key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		t.Fatalf("marshal test JWT public key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

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

// fakeResetPgPort is the loopback port the reset-data port-forward wait
// probes (dev.sh DEV_RESET_PG_PORT seam, real default 5432). A dedicated
// port keeps the fake test deterministic without touching a host PostgreSQL.
const fakeResetPgPort = "15432"

var (
	fakeResetPgOnce sync.Once
	fakeResetPgLn   net.Listener
	fakeResetPgErr  error
)

// fakeResetPostgres binds one shared loopback listener for the whole test
// package so every reset-data test's TCP probe succeeds (accepted and closed
// immediately, exactly like fakeOperatorGateway).
func fakeResetPostgres(t *testing.T) {
	t.Helper()
	fakeResetPgOnce.Do(func() {
		fakeResetPgLn, fakeResetPgErr = new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:"+fakeResetPgPort)
		if fakeResetPgErr != nil {
			return
		}
		go func() {
			for {
				conn, err := fakeResetPgLn.Accept()
				if err != nil {
					return
				}
				conn.Close()
			}
		}()
	})
	if fakeResetPgErr != nil {
		t.Fatalf("cannot bind fake reset postgres listener: %v", fakeResetPgErr)
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
		// Pin FIXTURE_VERSION so dev.sh skips its `go run ./cmd/devseed/
		// -print-fixture-version` preamble (dev.sh line 27). On a cold CI
		// build cache that probe compiles the whole devseed binary and takes
		// 12-30s+, which raced the 30s holder window in
		// TestLockConflictReportsHolder — by the time `down` reached
		// acquire_lock the holder's lock had already expired, so the conflict
		// was never reported. The real probe returns "v2", so this is
		// behavior-identical and makes every fake-CLI test deterministic.
		"FIXTURE_VERSION=v2",
		// Probe idle test ports, not the real 8082-8088: the actual dev
		// environment may be running on the host during tests.
		"DEV_PORTS_OVERRIDE=19082 19083 19084 19085 19086 19087 19088",
		// The operator gateway readiness is a TCP probe without an HTTP
		// endpoint to shim; point dev.sh's probe at the shared loopback
		// listener above.
		"DEV_OPERATOR_GATEWAY_PORT="+fakeOperatorGatewayPort,
		// The reset-data port-forward wait probes a loopback TCP port; point
		// it at the shared fake listener (real default 5432).
		"DEV_RESET_PG_PORT="+fakeResetPgPort,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	fakeOperatorGateway(t)
	fakeResetPostgres(t)
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

// dockerIPProbeShim returns the docker shim fragment that answers the
// per-customer-network IP probes agents_up performs: the management node and
// the registry are bridged into every customer network, and the agent overlay
// resolves its operator-gateway / registry hostAliases from those addresses.
//
// A docker shim that models "no managed object exists" must still answer these
// two probes. agents_up reads them from the cluster it is deploying into, and
// a fake that exits non-zero there aborts the stage under `set -e` before it
// can report anything — which is exactly how the agents_up deploy path stayed
// untested (D-iota / iota-1, 2026-09-27 design review).
//
// The fragment matches only the network-IP format string, so unrelated
// `--format` probes (the registry label/volume/state probes) keep their own
// answers.
func dockerIPProbeShim() string {
	return `if [ "$1" = "container" ] && [ "$2" = "inspect" ] && [ "$3" = "--format" ] && [[ "$4" == *NetworkSettings.Networks* ]]; then
  case "$5" in
    k3d-release-manager-control-server-0) printf '172.18.0.2\n'; exit 0 ;;
    k3d-release-manager-registry) printf '172.18.0.3\n'; exit 0 ;;
  esac
  exit 1
fi
`
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
	//
	// The holder keeps the flock until the test writes a release marker,
	// rather than a fixed sleep window: a fixed window races the second
	// process's startup under CI load (the devseed -print-fixture-version
	// probe used to compile the whole binary on a cold cache, 12-30s+, and
	// expire the window before `down` reached acquire_lock — TASK-065 CI
	// failure). The marker makes the hold deterministic.
	releasePath := filepath.Join(stateDir, "release-lock")
	holdCtx, holdCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer holdCancel()
	hold := exec.CommandContext(holdCtx, "bash", "-c", fmt.Sprintf(
		"source %q; acquire_lock down; while [ ! -f %q ]; do sleep 0.1; done",
		filepath.Join(repoRoot(t), "deploy", "dev", "lib", "lock.sh"), releasePath))
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
	// Release the holder now that the conflict is asserted; the flock drops
	// when the holder process exits (the deferred kill is a safety net).
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("write release marker: %v", err)
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

// TestRequireK3dReadsTheK3dVersionLine pins the k3d guard against the
// order-dependent parse it shipped with. `k3d version` prints two lines — k3d
// first, then the k3s it bundles — and the guard took the first vX.Y.Z match
// from the whole output. A k3d built by `go install` cannot embed its version
// and reports "v5-dev" (no patch component), so that match fell through to the
// k3s line and the guard rejected a perfectly good install with "k3d v1.21.7
// is too old", naming a version that is not k3d's at all and sending the
// operator after the wrong tool.
//
// The shims used by the other tests print the k3d line only, which is exactly
// why this went unnoticed; this one reproduces the real two-line output.
func TestRequireK3dReadsTheK3dVersionLine(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	writeShim(t, binDir, "flock", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "docker", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "k3d",
		"#!/usr/bin/env bash\nprintf 'k3d version v5-dev\\nk3s version v1.21.7-k3s1 (default)\\n'\n")
	writeShim(t, binDir, "curl", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "kubectl", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nexit 0\n")

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("expected the guard to reject an unparseable k3d version:\n%s", out)
	}
	if !strings.Contains(out, "k3d_unavailable") {
		t.Fatalf("expected k3d_unavailable:\n%s", out)
	}
	if !strings.Contains(out, "cannot determine k3d version") {
		t.Fatalf("expected an explicit version-parse failure:\n%s", out)
	}
	// The regression itself: the k3s line must never be reported as the k3d
	// version. "too old" is the confidently wrong conclusion this bug drew.
	if strings.Contains(out, "too old") {
		t.Fatalf("guard reported the k3s version as an outdated k3d:\n%s", out)
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
	// label contract. Every other verb exits 0 at the end of the shim — it
	// never reaches the real Docker daemon, so a purge inside a test cannot
	// touch the developer's containers (TASK-150 verification: the previous
	// comment claimed start/rm "pass through", which the code below does not
	// do).
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
	// REQ-065 AC-065-01: the JWT key helper is real crypto (Ed25519), so the go
	// shim delegates -ensure-jwt-keys to the real toolchain instead of emulating
	// it — dev.sh's wiring is then proven end to end against the helper that
	// cmd/devseed unit-tests. The path is resolved before the fake binDir is
	// prepended to the child PATH.
	realGo, lookErr := exec.LookPath("go")
	if lookErr != nil {
		t.Fatalf("resolve real go for the devseed shim: %v", lookErr)
	}
	writeShim(t, binDir, "go", fmt.Sprintf(`#!/usr/bin/env bash
if [ "$1" = "env" ]; then printf 'https://proxy.golang.org,direct\n'; exit 0; fi
for a in "$@"; do
  if [ "$a" = "-ensure-jwt-keys" ]; then exec %s "$@"; fi
done
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
`, realGo))
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
		if strings.HasPrefix(c, "release-manager-control|") && !strings.Contains(c, "8082-8088:30082-30088@loadbalancer") {
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
	// k3d writes kubeconfigs with a 0.0.0.0 API server for k3d-assigned
	// ports, which the host cannot dial; dev.sh rewrites it to 127.0.0.1
	// (real smoke 2026-08-27: customer_kubectl → openapi download refused).
	rawKC, err := os.ReadFile(filepath.Join(stateDir, "kubeconfigs", "dev-customer-a-direct.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawKC), "0.0.0.0:") {
		t.Fatalf("customer kubeconfig must not retain 0.0.0.0 server:\n%s", rawKC)
	}
	if !strings.Contains(string(rawKC), "https://127.0.0.1:12345") {
		t.Fatalf("customer kubeconfig server must be rewritten to 127.0.0.1:\n%s", rawKC)
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

// TestDevUpWaitsForReleaseAPI covers REQ-065 D2: cmd/api is part of the dev
// environment, so dev-up must wait for its rollout and probe its /readyz on the
// 8088 band entry. Without this, a manifest that silently dropped the api
// Deployment would leave its Ed25519 public-key verification uncovered while
// every other gate stayed green.
//
// Mutation check: removing `deployment/api` from the rollout list or the
// `require_readyz api` call makes this fail.
func TestDevUpWaitsForReleaseAPI(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// Log both the kubectl invocations and the probe URLs so the wiring is
	// observable; the fixture /version payload still has to answer.
	writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$DEV_DATA_DIR/kubectl.log"
for a in "$@"; do
  if [ "$a" = "port-forward" ]; then printf 'Forwarding from 127.0.0.1:18088 -> 8088\n'; sleep 30; exit 0; fi
  if [ "$a" = "redis-cli" ]; then printf 'PONG\n'; exit 0; fi
done
exit 0
`)
	writeShim(t, binDir, "curl", `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$DEV_DATA_DIR/curl.log"
if [[ "$*" == *"/version"* ]]; then printf '{"version":"fixture-v2"}\n'; exit 0; fi
exit 0
`)

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}

	kubectlLog, err := os.ReadFile(filepath.Join(stateDir, "kubectl.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kubectlLog), "deployment/api") {
		t.Fatalf("dev-up must wait for the api rollout:\n%s", kubectlLog)
	}
	curlLog, err := os.ReadFile(filepath.Join(stateDir, "curl.log"))
	if err != nil {
		t.Fatal(err)
	}
	// 19088 is the DEV_PORTS_OVERRIDE entry for the api band slot (8088).
	if !strings.Contains(string(curlLog), "19088/readyz") {
		t.Fatalf("dev-up must probe the api /readyz on the 8088 band slot:\n%s", curlLog)
	}
}

// capturingKubectlShim replaces the kubectl shim with one that records every
// apply's stdin (so tests can assert the applied image references) and still
// answers the port-forward / redis-cli probes the happy path needs. It pairs
// with manifestKustomizeShim: the default kustomize shim prints nothing, so
// there would be no image reference to inspect.
func capturingKubectlShim(t *testing.T, binDir string) {
	t.Helper()
	writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$DEV_DATA_DIR/kubectl.log"
if [[ "$*" == *"apply -f -"* ]]; then cat >> "$DEV_DATA_DIR/applied.yaml"; exit 0; fi
for a in "$@"; do
  if [ "$a" = "port-forward" ]; then printf 'Forwarding from 127.0.0.1:18088 -> 8088\n'; sleep 30; exit 0; fi
  if [ "$a" = "redis-cli" ]; then printf 'PONG\n'; exit 0; fi
done
exit 0
`)
}

// manifestKustomizeShim emits one Deployment carrying the registry image
// reference the repository manifests use, so an apply can be inspected.
func manifestKustomizeShim(t *testing.T, binDir string) {
	t.Helper()
	writeShim(t, binDir, "kustomize", `#!/usr/bin/env bash
cat <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: webhook
spec:
  template:
    spec:
      containers:
        - name: webhook
          image: localhost:5001/release-webhook:dev
YAML
`)
}

// TestRegistryPortOverrideDerivesEveryReference covers the concurrency defect
// this seam fixes: REGISTRY_PORT and the k3d control-plane API port were
// hardcoded, so a second dev environment on the same host could not start at
// all (`registry_unreachable: Bind for 0.0.0.0:5001 failed`). With the
// overrides, every derived value must follow the variables — the container
// publish, the applied image references, the containerd mirror and the k3d API
// port — so two sessions can coexist on distinct ports.
func TestRegistryPortOverrideDerivesEveryReference(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	manifestKustomizeShim(t, binDir)
	capturingKubectlShim(t, binDir)
	env = append(env, "REGISTRY_PORT=5009", "DEV_K3D_API_PORT=6449")

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up with port overrides failed:\n%s", out)
	}

	creates := strings.Join(clusterCreates(k3dCreates(stateDir)), "\n")
	if !strings.Contains(creates, "--api-port 127.0.0.1:6449") {
		t.Fatalf("k3d must bind the overridden API port:\n%s", creates)
	}
	if strings.Contains(creates, "--api-port 127.0.0.1:6443") {
		t.Fatalf("the hardcoded API port must be gone:\n%s", creates)
	}
	if strings.Contains(creates, "k3d/registries.yaml") || !strings.Contains(creates, "--registry-config /tmp/") {
		t.Fatalf("an overridden registry port must use a materialized mirror config:\n%s", creates)
	}
	mirror, err := os.ReadFile(filepath.Join(stateDir, "registry-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mirror), "localhost:5009") || strings.Contains(string(mirror), "localhost:5001") {
		t.Fatalf("the mirror key must follow REGISTRY_PORT:\n%s", mirror)
	}
	applied, err := os.ReadFile(filepath.Join(stateDir, "applied.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(applied), "localhost:5009/release-") {
		t.Fatalf("applied image references must follow REGISTRY_PORT:\n%s", applied)
	}
	if strings.Contains(string(applied), "localhost:5001/") {
		t.Fatalf("no applied reference may keep the hardcoded registry port:\n%s", applied)
	}
	dockerCreates, err := os.ReadFile(filepath.Join(stateDir, "docker-create.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerCreates), "127.0.0.1:5009:5000") {
		t.Fatalf("the registry container must publish the overridden port:\n%s", dockerCreates)
	}
}

// TestDefaultRegistryPortUsesTheRepositoryMirror pins the "defaults unchanged"
// half of the seam: without overrides the repository mirror file is used as-is
// and every reference keeps the documented localhost:5001.
func TestDefaultRegistryPortUsesTheRepositoryMirror(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	manifestKustomizeShim(t, binDir)
	capturingKubectlShim(t, binDir)

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("default dev-up failed:\n%s", out)
	}

	creates := strings.Join(clusterCreates(k3dCreates(stateDir)), "\n")
	if !strings.Contains(creates, "--api-port 127.0.0.1:6443") {
		t.Fatalf("the default API port must stay 6443:\n%s", creates)
	}
	if !strings.Contains(creates, "k3d/registries.yaml") || strings.Contains(creates, "--registry-config /tmp/") {
		t.Fatalf("the default path must use the repository mirror config:\n%s", creates)
	}
	mirror, err := os.ReadFile(filepath.Join(stateDir, "registry-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mirror), "localhost:5001") {
		t.Fatalf("the default mirror key must stay localhost:5001:\n%s", mirror)
	}
	applied, err := os.ReadFile(filepath.Join(stateDir, "applied.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(applied), "localhost:5001/release-") {
		t.Fatalf("default image references must stay localhost:5001:\n%s", applied)
	}
}

// TestImageBuildTimeoutFailsFastWithDiagnostics covers the robustness gap the
// 2026-09-24 run exposed: `docker build` has no deadline of its own, so a
// stalled build step makes dev-up wait forever (the release-api image sat in
// `RUN go mod download` and never returned). DEV_BUILD_TIMEOUT bounds each
// build and the failure names the image and the budget instead of hanging.
//
// Mutation check: dropping the 124/137 mapping (so a timeout degrades to the
// generic "build failed") makes this fail on the missing diagnostic.
func TestImageBuildTimeoutFailsFastWithDiagnostics(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// `build` outlives the budget; `manifest inspect` must fail so the image is
	// actually rebuilt rather than skipped as unchanged; every other verb keeps
	// the happy-path answer so the run reaches the images stage.
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "build" ]; then sleep 30; exit 0; fi
if [ "$1" = "manifest" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "container" ] && [ "$2" = "create" ]; then printf '%s\n' "$*" >> "$DEV_DATA_DIR/docker-create.log"; exit 0; fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then exit 1; fi
exit 0
`)
	env = append(env, "DEV_BUILD_TIMEOUT=1")

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("expected the build timeout to fail dev-up, got success:\n%s", out)
	}
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("expected exit 1, got %d:\n%s", code, out)
	}
	for _, want := range []string{"docker_build_failed", "timed out", "release-webhook"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in the timeout diagnostic:\n%s", want, out)
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
	// actually goes through docker build; build/push pass through. The
	// network-IP probes still answer: agents_up resolves the customer-agent
	// hostAliases from them.
	writeShim(t, binDir, "docker",
		"#!/usr/bin/env bash\n"+dockerIPProbeShim()+"if [ \"$1\" = \"manifest\" ] && [ \"$2\" = \"inspect\" ]; then exit 1; fi\nif [ \"$1\" = \"container\" ] && [ \"$2\" = \"inspect\" ]; then exit 1; fi\nif [ \"$1\" = \"network\" ] && [ \"$2\" = \"inspect\" ]; then exit 1; fi\nprintf '%s\\n' \"$*\" >> \""+stateDir+"/docker-calls.log\"\nexit 0\n")
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
		// The configured proxy is loopback-bound, so it cannot serve a RUN
		// step (127.0.0.1 there is the build container itself). BuildKit
		// forwards the client's proxy variables automatically, so dev.sh must
		// clear them explicitly or every fetch inside the step fails
		// (real smoke 2026-09-11: `npm ci` -> ECONNREFUSED 127.0.0.1:7890
		// while registry.npmjs.org answered 200 directly).
		if !strings.Contains(line, "--build-arg HTTP_PROXY= ") ||
			!strings.Contains(line, "--build-arg HTTPS_PROXY= ") ||
			!strings.Contains(line, "--build-arg http_proxy= ") ||
			!strings.Contains(line, "--build-arg https_proxy= ") {
			t.Fatalf("docker build did not clear the loopback proxy: %s", line)
		}
		if strings.Contains(line, "--build-arg HTTP_PROXY=http://127.0.0.1:7890") {
			t.Fatalf("docker build injected an unreachable loopback proxy: %s", line)
		}
		// Real smoke 2026-08-27: the google default module host is
		// unreachable directly from CN hosts — the build chain must prepend
		// the directly-reachable goproxy.cn as the primary entry.
		if !strings.Contains(line, "--build-arg GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct") {
			t.Fatalf("docker build missing GOPROXY build-arg: %s", line)
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

// TestClusterCreateInjectsReachableProxyEnv covers the other half of the proxy
// contract: a proxy a build container CAN reach is injected into the build
// args, and the GOPROXY host is exempted in NO_PROXY so module fetches bypass
// it (real smoke 2026-08-27).
func TestClusterCreateInjectsReachableProxyEnv(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	writeShim(t, binDir, "docker",
		"#!/usr/bin/env bash\n"+dockerIPProbeShim()+"if [ \"$1\" = \"manifest\" ] && [ \"$2\" = \"inspect\" ]; then exit 1; fi\nif [ \"$1\" = \"container\" ] && [ \"$2\" = \"inspect\" ]; then exit 1; fi\nif [ \"$1\" = \"network\" ] && [ \"$2\" = \"inspect\" ]; then exit 1; fi\nprintf '%s\\n' \"$*\" >> \""+stateDir+"/docker-calls.log\"\nexit 0\n")
	env = append(env, "HTTP_PROXY=http://proxy.corp.internal:3128", "HTTPS_PROXY=http://proxy.corp.internal:3128",
		"GOPROXY=", "DEV_DOCKER_MIRROR=docker.1ms.run/library/")

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
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
		if !strings.Contains(line, "--build-arg HTTP_PROXY=http://proxy.corp.internal:3128") ||
			!strings.Contains(line, "--build-arg HTTPS_PROXY=http://proxy.corp.internal:3128") {
			t.Fatalf("docker build missing a reachable proxy build-arg: %s", line)
		}
		if !strings.Contains(line, "--build-arg NO_PROXY=localhost,127.0.0.1,goproxy.cn") {
			t.Fatalf("docker build NO_PROXY missing the goproxy host exemption: %s", line)
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
		// A never-seeded environment reports no entities, not zeros shaped like
		// measurements.
		`"fixture_entities":{}`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %s:\n%s", want, out)
		}
	}
}

// TestStatusDerivesRestartTargetsFromTheControlPlane covers the D-029 D3
// contract: the REQ-066 restart targets are derived from the live management
// cluster, so a reshaped control plane cannot leave a stale Deployment name
// behind (the operator gateway was folded into the orchestrator container by
// TASK-065). Only the control-plane write path is a target: web, notifier and
// the datastores are present in the cluster but must not be restarted.
func TestStatusDerivesRestartTargetsFromTheControlPlane(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
cat <<'TABLE'
auth	8085 
notification-sink	8088 
notifier	8086 
orchestrator	8083 8084 
postgres	5432 
redis	6379 
web	8087 
webhook	8082 
TABLE
`)

	out, err := runDev(t, env, "status")
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	want := `"restart_targets":{"namespace":"release-manager-dev","deployments":["auth","orchestrator","webhook"]}`
	if !strings.Contains(out, want) {
		t.Fatalf("status output missing %s:\n%s", want, out)
	}
	// The exclusions are asserted on the restart_targets fragment alone: the
	// same names legitimately appear in the endpoints block above it.
	idx := strings.Index(out, `"restart_targets":`)
	if idx < 0 {
		t.Fatalf("status output has no restart_targets block:\n%s", out)
	}
	for _, unexpected := range []string{`"web"`, `"notifier"`, `"notification-sink"`, `"postgres"`, `"redis"`} {
		if strings.Contains(out[idx:], unexpected) {
			t.Fatalf("restart targets leaked %s:\n%s", unexpected, out)
		}
	}
}

// TestStatusProjectsFixtureEntitiesFromTheFixture covers the fixture_entities
// block. It used to be a hand-maintained list of eight names, four of which
// (operator_sessions, bundles, bootstrap_installs, values_revisions) the fixture
// does not have, so half the block reported a false zero however it was seeded.
// It is now a projection of the fixture's own keys.
//
// The fixture mixes two object shapes and the projection has to tell them apart:
// a keyed collection counts its entries, while a single resource (bundle) has
// scalar fields and denotes one entity. Counting bundle's keys would report 2 for
// the one bundle the fixture defines.
func TestStatusProjectsFixtureEntitiesFromTheFixture(t *testing.T) {
	stateDir := t.TempDir()
	env, _ := fakeEnv(t, stateDir)
	fixture := `{
	  "fixture_version": "fixture-v2",
	  "generated_at": "2026-01-01T00:00:00Z",
	  "customers": {"dev-customer-a": {}, "dev-customer-b": {}},
	  "clusters": {"a": {}, "b": {}, "c": {}, "d": {}},
	  "routes": {"r1": {}, "r2": {}, "r3": {}},
	  "definitions": {"d1": {}, "d2": {}},
	  "bundle": {"id": "bundle-1", "values_revision_id": "vr-1"}
	}`
	if err := os.WriteFile(filepath.Join(stateDir, "dev-fixture.json"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runDev(t, env, "status")
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	// Asserted as one block so the test also pins what is absent: the metadata
	// keys are not entities, the dead names are gone, and bundle counts one
	// resource rather than its two fields.
	want := `"fixture_entities":{"customers":2,"clusters":4,"routes":3,"definitions":2,"bundle":1}`
	if !strings.Contains(out, want) {
		t.Fatalf("status output missing %s:\n%s", want, out)
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

// TestResetDataSplitsSeedAroundEnrollmentAndDeploysAgents locks the
// reset-data split-seed contract (D-017 residual, real smoke 2026-08-27):
// reset-data rebuilds the customer clusters from zero, so they hold no
// operator agents and the bootstrap INSTALL would fail the artifact stage
// with "no operator for cluster ..." — the re-seed must enroll first
// (--reset --stop-after enrollment), deploy the agents (agents_up), and only
// then resume install + verify. The fake `go` shim records every devseed
// invocation so the test asserts the exact two-leg CLI contract.
func TestResetDataSplitsSeedAroundEnrollmentAndDeploysAgents(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// Override the happy go shim: record each devseed invocation to
	// devseed-calls.log, print a fixture version for the -print-fixture-version
	// probe, and write the four enrollment tokens on the --stop-after
	// enrollment leg (the split-seed agents_up stage consumes them).
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
if [[ "$*" == *"-print-fixture-version"* ]]; then printf 'v22\n'; exit 0; fi
printf '%s\n' "$*" >> "$DEV_DATA_DIR/devseed-calls.log"
if [[ "$*" == *"--stop-after enrollment"* ]]; then
  mkdir -p "$DEV_DATA_DIR/dev-enrollment-tokens"
  for c in dev-customer-a-direct dev-customer-a-cache dev-customer-b-replicated dev-customer-b-mixed; do
    printf 'fake-token\n' > "$DEV_DATA_DIR/dev-enrollment-tokens/$c.token"
  done
fi
exit 0
`)
	writeShim(t, binDir, "pg_dump", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "pg_restore", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "psql", "#!/usr/bin/env bash\nexit 0\n")
	// reset-data rebuilds the customer clusters and then really deploys the
	// agents, so the dev mTLS CA a prior dev-up generated must be on disk
	// (agents_up copies it into the agent overlay's gateway-CA secret). The
	// fixture never generates it: reset-data, like the real target, assumes
	// dev-up already did.
	caDir := filepath.Join(stateDir, "dev-ca")
	if err := os.MkdirAll(caDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.key"), []byte("fake-ca-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.crt"), []byte("fake-ca-cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	env = append(env, "CONFIRM=1")

	out, err := runDev(t, env, "reset-data")
	if err != nil {
		t.Fatalf("reset-data failed:\n%s", out)
	}
	callsPath := filepath.Join(stateDir, "devseed-calls.log")
	data, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("devseed-calls.log missing: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected exactly 2 devseed seed invocations, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "--reset") ||
		!strings.Contains(lines[0], "--stop-after enrollment") ||
		!strings.Contains(lines[0], "--database-dsn") {
		t.Fatalf("first devseed leg must be --reset --stop-after enrollment with --database-dsn, got:\n%s", lines[0])
	}
	if strings.Contains(lines[1], "--reset") ||
		strings.Contains(lines[1], "--stop-after") ||
		strings.Contains(lines[1], "--database-dsn") {
		t.Fatalf("second devseed leg must be a plain resume (no --reset/--stop-after/--database-dsn), got:\n%s", lines[1])
	}
}

// TestAgentsUpDeploysOperatorWithCorrectKubectlContract drives the real
// agents_up deploy path (customer clusters with no operator deployment →
// agents_deployed false) and locks the customer_kubectl arg contract: the
// cluster id is consumed for the KUBECONFIG path only and must never reach
// kubectl's argv (real smoke 2026-08-27: `unknown command
// "dev-customer-a-direct" for "kubectl"`). The kubectl shim exits 42 on any
// cluster id in its argv and records every call with its KUBECONFIG.
func TestAgentsUpDeploysOperatorWithCorrectKubectlContract(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// dev-seed preconditions: JWT key, webhook service token and dev mTLS CA
	// (generated by a prior dev-up, D3 / 批次3 D2 / 批次5 D1).
	writeDevJWTKeyFiles(t, stateDir, testJWTPrivateKeyPEM(t), "")
	tokenPath := filepath.Join(stateDir, "dev-service-tokens", "webhook-service-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("service-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"ci-api-key": "ci-api-key-value", "harbor-service-token": "harbor-service-token-value", "notifier-service-token": "notifier-service-token-value"} {
		extra := filepath.Join(filepath.Dir(tokenPath), name)
		if err := os.WriteFile(extra, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	caDir := filepath.Join(stateDir, "dev-ca")
	if err := os.MkdirAll(caDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.key"), []byte("ca-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.crt"), []byte("ca-cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	// devseed shim: record invocations and write the four enrollment tokens
	// (agents_up consumes them).
	writeShim(t, binDir, "go", `#!/usr/bin/env bash
if [ "$1" = "env" ]; then printf 'https://proxy.golang.org,direct\n'; exit 0; fi
printf '%s\n' "$*" >> "$DEV_DATA_DIR/go-calls.log"
if [[ "$*" == *"-print-fixture-version"* ]]; then printf 'v22\n'; exit 0; fi
mkdir -p "$DEV_DATA_DIR/dev-enrollment-tokens"
for c in dev-customer-a-direct dev-customer-a-cache dev-customer-b-replicated dev-customer-b-mixed; do
  printf 'fake-token\n' > "$DEV_DATA_DIR/dev-enrollment-tokens/$c.token"
done
exit 0
`)
	// docker: answer the per-network IP probes with deterministic addresses
	// derived from the network name in the --format string, so the test can
	// assert agents_up substitutes each cluster's OWN management/registry IP
	// (the {{range}} concatenation bug — real smoke 2026-08-27 — is caught
	// by the per-cluster assertions below).
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then
  if [ "$3" = "--format" ]; then
    net="$(printf '%s' "$4" | sed -nE 's#.*"k3d-([^"]+)".*#\1#p')"
    case "${5}:${net}" in
      k3d-release-manager-control-server-0:dev-customer-a-direct) printf '10.1.0.2\n'; exit 0 ;;
      k3d-release-manager-control-server-0:dev-customer-a-cache) printf '10.2.0.2\n'; exit 0 ;;
      k3d-release-manager-control-server-0:dev-customer-b-replicated) printf '10.3.0.2\n'; exit 0 ;;
      k3d-release-manager-control-server-0:dev-customer-b-mixed) printf '10.4.0.2\n'; exit 0 ;;
      k3d-release-manager-registry:dev-customer-a-direct) printf '10.1.0.3\n'; exit 0 ;;
      k3d-release-manager-registry:dev-customer-a-cache) printf '10.2.0.3\n'; exit 0 ;;
      k3d-release-manager-registry:dev-customer-b-replicated) printf '10.3.0.3\n'; exit 0 ;;
      k3d-release-manager-registry:dev-customer-b-mixed) printf '10.4.0.3\n'; exit 0 ;;
      *) exit 1 ;;
    esac
  fi
  exit 1
fi
exit 0
`)
	// kubectl: exit 42 when a cluster id leaks into the argv position (the
	// customer_kubectl contract — a leaked id lands as the first kubectl
	// subcommand), report the customer operator deployments as absent so
	// agents_up actually deploys, capture every applied manifest keyed by
	// cluster, and record every call with its KUBECONFIG.
	writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
case "$1" in
  dev-customer-*|release-manager-*) printf 'kubectl received cluster as subcommand: %s\n' "$*" >&2; exit 42 ;;
esac
printf 'kc|%s|%s\n' "${KUBECONFIG:-}" "$*" >> "$DEV_DATA_DIR/kubectl-calls.log"
for a in "$@"; do
  if [ "$a" = "port-forward" ]; then printf 'Forwarding from 127.0.0.1:18088 -> 8088\n'; sleep 30; exit 0; fi
done
if [[ "$*" == *"get deployment operator"* ]]; then exit 1; fi
# Capture the agent manifest apply: the create-secret pipelines pipe EMPTY
# output through apply here (the create shim emits nothing), so only a
# non-empty stdin is recorded — that is exactly the agent manifest.
if [ "$1" = "apply" ] && [ "$2" = "-f" ] && [ "$3" = "-" ]; then
  mkdir -p "$DEV_DATA_DIR/applied"
  cluster="$(basename "${KUBECONFIG:-unknown}" .yaml)"
  if IFS= read -r first_line; then
    printf '%s\n' "$first_line" > "$DEV_DATA_DIR/applied/$cluster.yaml"
    cat >> "$DEV_DATA_DIR/applied/$cluster.yaml"
  fi
  exit 0
fi
exit 0
`)
	// kustomize emits the base agent manifest carrying the hostAliases
	// placeholders agents_up substitutes per cluster.
	writeShim(t, binDir, "kustomize", `#!/usr/bin/env bash
printf 'apiVersion: v1\nkind: Pod\nmetadata:\n  name: operator\nspec:\n  hostAliases:\n  - ip: "172.18.0.2"\n    hostnames:\n    - operator-gateway.dev.release-manager.local\n  - ip: "172.18.0.3"\n    hostnames:\n    - registry.dev.release-manager.local\n'
`)
	env = append(env, "DEV_TIMEOUT_OPERATOR=42", "DEV_TIMEOUT_SEED_RETRIES=1")

	out, err := runDev(t, env, "seed")
	if err != nil {
		t.Fatalf("seed failed:\n%s", out)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "kubectl-calls.log"))
	if err != nil {
		t.Fatalf("kubectl-calls.log missing: %v\n%s", err, out)
	}
	calls := strings.TrimSpace(string(data))
	if !strings.Contains(calls, "apply -f -") {
		t.Fatalf("expected agents_up to kubectl apply the agent manifest:\n%s", calls)
	}
	for _, line := range strings.Split(calls, "\n") {
		// kc|<kubeconfig>|<argv> — the argv must never start with a cluster id.
		fields := strings.SplitN(line, "|", 3)
		if len(fields) != 3 {
			continue
		}
		if strings.HasPrefix(fields[2], "dev-customer-") || strings.HasPrefix(fields[2], "release-manager-") {
			t.Fatalf("cluster id leaked into kubectl argv: %s", line)
		}
	}
	// Per-cluster hostAliases: each cluster's applied manifest carries its
	// OWN management node (10.<i>.0.2) and registry (10.<i>.0.3) addresses —
	// never another cluster's (would catch the {{range}} concatenation).
	for i, cluster := range []string{
		"dev-customer-a-direct", "dev-customer-a-cache", "dev-customer-b-replicated", "dev-customer-b-mixed",
	} {
		applied, err := os.ReadFile(filepath.Join(stateDir, "applied", cluster+".yaml"))
		if err != nil {
			t.Fatalf("applied manifest for %s missing: %v\nkc log:\n%s", cluster, err, calls)
		}
		wantMgmt := fmt.Sprintf(`"10.%d.0.2"`, i+1)
		wantReg := fmt.Sprintf(`"10.%d.0.3"`, i+1)
		if !strings.Contains(string(applied), wantMgmt) || !strings.Contains(string(applied), wantReg) {
			t.Fatalf("applied manifest for %s must carry %s and %s:\n%s", cluster, wantMgmt, wantReg, applied)
		}
		for j := range []int{1, 2, 3, 4} {
			if j == i {
				continue
			}
			if strings.Contains(string(applied), fmt.Sprintf(`"10.%d.0.2"`, j+1)) ||
				strings.Contains(string(applied), fmt.Sprintf(`"10.%d.0.3"`, j+1)) {
				t.Fatalf("applied manifest for %s leaked another cluster's IP:\n%s", cluster, applied)
			}
		}
	}
}

// resetDataAgentsEnv prepares a reset-data run whose interesting stage is
// agents_up. reset-data is the flow that reaches agents_up with IMAGE_TAGS
// populated (it calls images_up first), so the operator's content digest is
// available for the skip decision.
//
// The kubectl shim answers the operator Deployment probe from
// $DEV_DATA_DIR/operator-image.txt — absent file means "no Deployment" — and
// records every non-empty `apply -f -` in $DEV_DATA_DIR/agent-applies.log. The
// empty `kubectl create secret | kubectl apply -f -` pipelines are not
// recorded, exactly like a real cluster.
func resetDataAgentsEnv(t *testing.T) (stateDir string, env []string) {
	t.Helper()
	stateDir = t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	writeShim(t, binDir, "go", `#!/usr/bin/env bash
if [ "$1" = "env" ]; then printf 'https://proxy.golang.org,direct\n'; exit 0; fi
if [[ "$*" == *"-print-fixture-version"* ]]; then printf 'v22\n'; exit 0; fi
if [[ "$*" == *"--stop-after enrollment"* ]]; then
  mkdir -p "$DEV_DATA_DIR/dev-enrollment-tokens"
  for c in dev-customer-a-direct dev-customer-a-cache dev-customer-b-replicated dev-customer-b-mixed; do
    printf 'fake-token\n' > "$DEV_DATA_DIR/dev-enrollment-tokens/$c.token"
  done
fi
exit 0
`)
	writeShim(t, binDir, "pg_dump", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "pg_restore", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "psql", "#!/usr/bin/env bash\nexit 0\n")
	// The customer-agent overlay carries the operator image under its static
	// `:dev` tag; agents_up substitutes the recorded content digest into it.
	writeShim(t, binDir, "kustomize", `#!/usr/bin/env bash
cat <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: operator
spec:
  template:
    spec:
      containers:
      - name: operator
        image: localhost:5001/release-operator:dev
      hostAliases:
      - ip: "172.18.0.2"
        hostnames:
        - operator-gateway.dev.release-manager.local
      - ip: "172.18.0.3"
        hostnames:
        - registry.dev.release-manager.local
YAML
`)
	writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
for a in "$@"; do
  if [ "$a" = "port-forward" ]; then printf 'Forwarding from 127.0.0.1:18088 -> 8088\n'; sleep 30; exit 0; fi
done
if [[ "$*" == *"get deployment operator"* ]]; then
  if [ -s "$DEV_DATA_DIR/operator-image.txt" ]; then cat "$DEV_DATA_DIR/operator-image.txt"; exit 0; fi
  exit 1
fi
if [ "$1" = "apply" ] && [ "$2" = "-f" ] && [ "$3" = "-" ]; then
  if IFS= read -r first_line; then
    cluster="$(basename "${KUBECONFIG:-unknown}" .yaml)"
    mkdir -p "$DEV_DATA_DIR/applied"
    printf '%s\n' "$first_line" > "$DEV_DATA_DIR/applied/$cluster.yaml"
    cat >> "$DEV_DATA_DIR/applied/$cluster.yaml"
    printf '%s\n' "$cluster" >> "$DEV_DATA_DIR/agent-applies.log"
  fi
  exit 0
fi
exit 0
`)
	// A prior dev-up generated the dev mTLS CA that agents_up copies into the
	// agent overlay's gateway-CA secret.
	caDir := filepath.Join(stateDir, "dev-ca")
	if err := os.MkdirAll(caDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.key"), []byte("fake-ca-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.crt"), []byte("fake-ca-cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	return stateDir, append(env, "CONFIRM=1")
}

// agentApplyCount counts the customer-agent manifest applies recorded by the
// kubectl shim.
func agentApplyCount(t *testing.T, stateDir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "agent-applies.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return len(strings.Fields(string(data)))
}

// operatorDigestFromApplied reads the operator image digest agents_up pinned
// into the applied agent manifest.
func operatorDigestFromApplied(t *testing.T, stateDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "applied", "dev-customer-a-direct.yaml"))
	if err != nil {
		t.Fatalf("applied agent manifest missing: %v", err)
	}
	match := regexp.MustCompile(`release-operator:content-sha256-([0-9a-f]{64})`).FindStringSubmatch(string(data))
	if match == nil {
		t.Fatalf("applied agent manifest is not pinned to an operator digest:\n%s", data)
	}
	return match[1]
}

// TestAgentsUpSkipsOnlyWhenOperatorImageDigestMatches is the regression for
// D-iota / iota-1: agents_deployed used to skip whenever the operator
// Deployment existed, so a rebuilt operator image never reached the customer
// clusters while the management plane rolled forward — every "change operator
// code, re-run dev-up, observe" loop silently observed the old code.
//
// reset-data runs images_up before agents_up, so IMAGE_TAGS carries the
// operator's content digest and the skip decision can be driven in both
// directions by a kubectl shim that reports the Deployment's image:
//
//	phase 1  Deployment absent                 -> agents_up applies
//	phase 2  Deployment at the recorded digest -> agents_up skips
//	phase 3  Deployment at a stale digest      -> agents_up applies again
//
// Phase 3 is the negative control: restore the existence-only check and it
// reports "already deployed", so no apply is recorded and the phase fails.
func TestAgentsUpSkipsOnlyWhenOperatorImageDigestMatches(t *testing.T) {
	stateDir, env := resetDataAgentsEnv(t)

	// Phase 1: no Deployment yet -> the agents must be deployed.
	out, err := runDev(t, env, "reset-data")
	if err != nil {
		t.Fatalf("reset-data (phase 1) failed:\n%s", out)
	}
	if got := agentApplyCount(t, stateDir); got != len(customerClustersForTest) {
		t.Fatalf("phase 1: expected %d agent manifest applies, got %d", len(customerClustersForTest), got)
	}
	digest := operatorDigestFromApplied(t, stateDir)

	// Phase 2: the Deployment already runs this run's digest -> skip.
	if err := os.WriteFile(filepath.Join(stateDir, "operator-image.txt"),
		[]byte("localhost:5001/release-operator:content-sha256-"+digest+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(stateDir, "agent-applies.log")); err != nil {
		t.Fatal(err)
	}
	out, err = runDev(t, env, "reset-data")
	if err != nil {
		t.Fatalf("reset-data (phase 2) failed:\n%s", out)
	}
	if !strings.Contains(out, "customer agents (already deployed)") {
		t.Fatalf("phase 2: a matching digest must skip the agent deploy:\n%s", out)
	}
	if got := agentApplyCount(t, stateDir); got != 0 {
		t.Fatalf("phase 2: expected no agent manifest applies, got %d", got)
	}

	// Phase 3 (negative control): a stale digest -> re-apply.
	if err := os.WriteFile(filepath.Join(stateDir, "operator-image.txt"),
		[]byte("localhost:5001/release-operator:content-sha256-"+strings.Repeat("0", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runDev(t, env, "reset-data")
	if err != nil {
		t.Fatalf("reset-data (phase 3) failed:\n%s", out)
	}
	if got := agentApplyCount(t, stateDir); got != len(customerClustersForTest) {
		t.Fatalf("phase 3: a stale operator image must be re-applied (%d applies recorded):\n%s",
			got, out)
	}
	if again := operatorDigestFromApplied(t, stateDir); again != digest {
		t.Fatalf("phase 3: re-applied manifest pinned %s, want %s", again, digest)
	}
}

// customerClustersForTest mirrors dev.sh's CUSTOMER_CLUSTERS.
var customerClustersForTest = []string{
	"dev-customer-a-direct", "dev-customer-a-cache", "dev-customer-b-replicated", "dev-customer-b-mixed",
}

// TestSeedLegRetriesTransientDevseedFailure locks the run_seed_leg retry
// contract (real smoke 2026-08-27: `get init status: unavailable: unexpected
// EOF` — a fresh seed connection routed to a just-terminated pod right after
// the maintenance rollout, despite require_readyz already seeing 200). The
// fake devseed fails the enrollment leg once with a transient error, then
// succeeds; the test asserts the leg was retried and the seed converged.
func TestSeedLegRetriesTransientDevseedFailure(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// dev-seed preconditions: JWT key, webhook service token and dev mTLS CA.
	writeDevJWTKeyFiles(t, stateDir, testJWTPrivateKeyPEM(t), "")
	tokenPath := filepath.Join(stateDir, "dev-service-tokens", "webhook-service-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("service-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"ci-api-key": "ci-api-key-value", "harbor-service-token": "harbor-service-token-value", "notifier-service-token": "notifier-service-token-value"} {
		extra := filepath.Join(filepath.Dir(tokenPath), name)
		if err := os.WriteFile(extra, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	caDir := filepath.Join(stateDir, "dev-ca")
	if err := os.MkdirAll(caDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.key"), []byte("ca-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.crt"), []byte("ca-cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	// devseed shim: record invocations, fail the FIRST enrollment leg with a
	// transient unavailable error, then succeed and write the tokens.
	writeShim(t, binDir, "go", `#!/usr/bin/env bash
if [ "$1" = "env" ]; then printf 'https://proxy.golang.org,direct\n'; exit 0; fi
if [[ "$*" == *"-print-fixture-version"* ]]; then printf 'v22\n'; exit 0; fi
printf '%s\n' "$*" >> "$DEV_DATA_DIR/go-calls.log"
if [[ "$*" == *"--stop-after enrollment"* ]]; then
  if [ ! -f "$DEV_DATA_DIR/enroll-failed" ]; then
    touch "$DEV_DATA_DIR/enroll-failed"
    printf 'get init status: unavailable: unexpected EOF\n' >&2
    exit 1
  fi
  mkdir -p "$DEV_DATA_DIR/dev-enrollment-tokens"
  for c in dev-customer-a-direct dev-customer-a-cache dev-customer-b-replicated dev-customer-b-mixed; do
    printf 'fake-token\n' > "$DEV_DATA_DIR/dev-enrollment-tokens/$c.token"
  done
fi
exit 0
`)
	env = append(env, "DEV_SEED_RETRY_DELAY=0")

	out, err := runDev(t, env, "seed")
	if err != nil {
		t.Fatalf("seed failed despite the transient being retried:\n%s", out)
	}
	logged, err := os.ReadFile(filepath.Join(stateDir, "go-calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	enrollCount := strings.Count(string(logged), "--stop-after enrollment")
	if enrollCount != 2 {
		t.Fatalf("expected the enrollment leg to be retried exactly once (2 invocations), got %d:\n%s", enrollCount, logged)
	}
	// 3 seed invocations total: 2 for the retried enrollment leg + 1 for the
	// plain resume leg (which must NOT be retried when it succeeds).
	totalLines := len(strings.Split(strings.TrimSpace(string(logged)), "\n"))
	if totalLines != 3 {
		t.Fatalf("expected 3 seed invocations (2 enrollment + 1 resume), got %d:\n%s", totalLines, logged)
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
	env = append(env, "DEV_PROFILE=ci", "E2E_RUN_ID=ci-run-001", "DEV_JWT_PRIVATE_KEY="+testJWTPrivateKeyPEM(t), "DEV_WEBHOOK_SERVICE_TOKEN=ci-service-token", "DEV_CI_API_KEY=ci-api-key-value", "DEV_HARBOR_SERVICE_TOKEN=ci-harbor-token", "DEV_NOTIFIER_SERVICE_TOKEN=ci-notifier-token",
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
	env2 = append(env2, "DEV_PROFILE=ci", "E2E_RUN_ID=ci-run-002", "DEV_JWT_PRIVATE_KEY="+testJWTPrivateKeyPEM(t), "DEV_WEBHOOK_SERVICE_TOKEN=ci-service-token", "DEV_CI_API_KEY=ci-api-key-value", "DEV_HARBOR_SERVICE_TOKEN=ci-harbor-token", "DEV_NOTIFIER_SERVICE_TOKEN=ci-notifier-token",
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
	writeShim(t, binDir, "docker", "#!/usr/bin/env bash\n"+dockerIPProbeShim()+"for a in \"$@\"; do if [ \"$a\" = \"inspect\" ]; then exit 1; fi; done\nexit 0\n")
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

// AC-065-22 (批次2 D2): a k3d cluster carries no Docker ownership label (k3d
// sets its own), so data/dev-ownership.json's k3d_clusters[] is the only
// ownership evidence. A same-named cluster that is NOT in that whitelist
// belongs to someone else and must fail resource_conflict — never be silently
// reused. The previous behaviour logged "(exists)" and returned 0, so dev-up
// converged on top of a foreign cluster it must never touch or delete.
func TestForeignSameNameK3dClusterConflicts(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)

	// A cluster with a managed name already exists ...
	if err := os.WriteFile(filepath.Join(stateDir, "clusters.txt"),
		[]byte("release-manager-control\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// ... but this run's ownership manifest does not list it.
	manifest := `{"profile":"local","created_at":"2026-01-01T00:00:00Z","fixture_version":"v2",` +
		`"k3d_clusters":[],"docker_containers":[],"docker_networks":[]}` + "\n"
	if err := os.WriteFile(filepath.Join(stateDir, "dev-ownership.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("expected resource_conflict for a foreign cluster, got success:\n%s", out)
	}
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("expected exit 1, got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "resource_conflict") {
		t.Fatalf("expected resource_conflict in stderr:\n%s", out)
	}
	if !strings.Contains(out, "release-manager-control") {
		t.Fatalf("the conflict must name the foreign cluster:\n%s", out)
	}
	// The foreign cluster must not be adopted into the whitelist, and no
	// cluster may be created in its place.
	after, readErr := os.ReadFile(filepath.Join(stateDir, "dev-ownership.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(after), "release-manager-control") {
		t.Fatalf("a foreign cluster must never be adopted into the whitelist:\n%s", after)
	}
	if creates := clusterCreates(k3dCreates(stateDir)); len(creates) != 0 {
		t.Fatalf("a conflicting run must not create clusters, got %v", creates)
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
		"dev-jwt/jwt-private-key.pem",
		"dev-jwt/jwt-public-key.pem",
		"dev-service-tokens/webhook-service-token",
		"dev-service-tokens/ci-api-key",
		"dev-service-tokens/harbor-service-token",
		"dev-service-tokens/notifier-service-token",
		"dev-enrollment-tokens/dev-customer-a-direct.token",
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

// TestJWTKeyPairGeneratedAndReused covers REQ-065 AC-065-01 / D3: local dev-up
// generates a PKCS#8 Ed25519 private key (0600) plus its derived public half
// under data/dev-jwt/ before apply, reuses them on re-runs, and rotation
// (delete + re-run) produces a fresh pair.
func TestJWTKeyPairGeneratedAndReused(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}
	// fakeEnv points DEV_DATA_DIR at the state dir: the pair lives at
	// <stateDir>/dev-jwt/jwt-{private,public}-key.pem.
	privatePath := filepath.Join(stateDir, "dev-jwt", "jwt-private-key.pem")
	publicPath := filepath.Join(stateDir, "dev-jwt", "jwt-public-key.pem")

	privateInfo, err := os.Stat(privatePath)
	if err != nil {
		t.Fatalf("jwt private key not generated: %v", err)
	}
	if privateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 jwt private key, got %o", privateInfo.Mode().Perm())
	}
	privatePEM, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	publicPEM, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatalf("jwt public key not generated: %v", err)
	}

	// Both halves must parse through the parsers the services use, and form a
	// pair: a verifier handed a mismatched public key would reject every token
	// the issuer signs.
	privateKey, err := jwtauth.ParseEd25519PrivateKeyPEM(string(privatePEM))
	if err != nil {
		t.Fatalf("generated private key fails the auth parser: %v", err)
	}
	publicKey, err := jwtauth.ParseEd25519PublicKeyPEM(string(publicPEM))
	if err != nil {
		t.Fatalf("generated public key fails the orchestrator parser: %v", err)
	}
	if !publicKey.Equal(privateKey.Public()) {
		t.Fatal("generated public key does not match the private key")
	}

	// Re-run reuses the same pair (no regeneration).
	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("second dev-up failed:\n%s", out)
	}
	second, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(privatePEM, second) {
		t.Fatal("jwt key regenerated on re-run; reuse contract violated")
	}

	// Rotation: delete the pair and re-run — a fresh pair appears.
	if err := os.RemoveAll(filepath.Join(stateDir, "dev-jwt")); err != nil {
		t.Fatal(err)
	}
	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up after rotation failed:\n%s", out)
	}
	third, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(privatePEM, third) {
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
if [ "$1" = "container" ] && [ "$2" = "inspect" ] && [ "$3" = "--format" ] && [[ "$4" == *NetworkSettings.Networks* ]]; then
  case "$5" in
    k3d-release-manager-control-server-0) printf '172.18.0.2\n'; exit 0 ;;
    k3d-release-manager-registry) printf '172.18.0.3\n'; exit 0 ;;
  esac
  exit 1
fi
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
	writeDevJWTKeyFiles(t, stateDir, testJWTPrivateKeyPEM(t), "")
	tokenPath := filepath.Join(stateDir, "dev-service-tokens", "webhook-service-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("service-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"ci-api-key": "ci-api-key-value", "harbor-service-token": "harbor-service-token-value", "notifier-service-token": "notifier-service-token-value"} {
		extra := filepath.Join(filepath.Dir(tokenPath), name)
		if err := os.WriteFile(extra, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
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

// runJwtHelper sources deploy/dev/lib/jwt.sh and evaluates one expression,
// returning its trimmed stdout. The smoke script uses the same library, so
// these tests guard the exact helper the smoke asserts with.
func runJwtHelper(t *testing.T, expr string) string {
	t.Helper()
	lib := filepath.Join(repoRoot(t), "deploy", "dev", "lib", "jwt.sh")
	script := "set -euo pipefail\nsource " + lib + "\n" + expr + "\n"
	out, err := exec.CommandContext(context.Background(), "bash", "-c", script).Output()
	if err != nil {
		t.Fatalf("jwt helper %q failed: %v", expr, err)
	}
	return strings.TrimSpace(string(out))
}

// TestJwtAlgHelperDetectsTheSigningAlgorithm is the falsifiable core of the
// smoke's EdDSA assertion (REQ-065 AC-065-01 / TASK-065 D1=A). It pins that the
// helper reports the header algorithm — including HS256 — so a revert to
// symmetric signing cannot pass the smoke unnoticed.
//
// Mutation check: making jwt_alg return "EdDSA" unconditionally (or reading the
// token length instead of the header) makes the HS256 case fail here.
func TestJwtAlgHelperDetectsTheSigningAlgorithm(t *testing.T) {
	b64 := func(header string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(header))
	}
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{"eddsa header", b64(`{"alg":"EdDSA","typ":"JWT"}`) + ".e30.sig", "EdDSA"},
		{"hs256 header", b64(`{"alg":"HS256","typ":"JWT"}`) + ".e30.sig", "HS256"},
		{"hs256 with key order reversed", b64(`{"typ":"JWT","alg":"HS256"}`) + ".e30.sig", "HS256"},
		{"header without alg", b64(`{"typ":"JWT"}`) + ".e30.sig", ""},
		{"malformed token", "not-a-jwt", ""},
		{"empty token", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runJwtHelper(t, "jwt_alg '"+tt.token+"'")
			if got != tt.want {
				t.Fatalf("jwt_alg(%q) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}

// TestJwtHs256ProbeTokenIsAWellFormedHs256Jwt proves the smoke's downgrade
// probe is a *genuine* HS256 token: three segments, an HS256 header and a
// signature that verifies with the secret it was signed with. Otherwise a
// service rejection could just mean "malformed token" and the probe would
// assert nothing about the algorithm.
func TestJwtHs256ProbeTokenIsAWellFormedHs256Jwt(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed: the HMAC probe cannot be built")
	}
	const secret = "change-me-in-production"
	token := runJwtHelper(t, "jwt_hs256_token "+secret)

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a three-segment JWT, got %d segments: %q", len(parts), token)
	}
	if alg := runJwtHelper(t, "jwt_alg '"+token+"'"); alg != "HS256" {
		t.Fatalf("probe header alg = %q, want HS256", alg)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if parts[2] != want {
		t.Fatalf("probe signature does not verify with its secret: got %q want %q", parts[2], want)
	}
}

// TestLengthOnlyAssertionWouldMissHs256 is the negative evidence for the
// assertion the smoke actually uses. An HS256 token's length is fully
// controllable through its payload, so a length check cannot be written
// robustly:
//
//   - a pinned equality is brittle (450 is the length this session observed,
//     but it is a property of the e2e-runner claims, not of EdDSA — and for
//     this header shape an HS256 token can only reach lengths ≡ 1 (mod 4), so
//     450 is not even reachable for it);
//   - any threshold or range check is fooled by padding the payload.
//
// This test proves the second point concretely: HS256 tokens can be produced
// with lengths straddling the observed EdDSA length (below AND above), while
// remaining HS256. Asserting the header algorithm is what closes the hole.
func TestLengthOnlyAssertionWouldMissHs256(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed: the HMAC probe cannot be built")
	}
	// 450 is the access-token length the Ed25519 migration produces (407 under
	// the previous HS256 scheme).
	const eddsaTokenLen = 450

	lengthOf := func(pad int) (int, string) {
		claims := fmt.Sprintf(`{"sub":"downgrade-probe","pad":%q}`, strings.Repeat("x", pad))
		token := runJwtHelper(t, fmt.Sprintf("jwt_hs256_token change-me-in-production '%s'", claims))
		return len(token), token
	}

	var below, above string
	for pad := 0; pad <= 400 && (below == "" || above == ""); pad++ {
		n, token := lengthOf(pad)
		if n < eddsaTokenLen && below == "" {
			below = token
		}
		if n > eddsaTokenLen && above == "" {
			above = token
		}
	}
	if below == "" || above == "" {
		t.Skip("could not produce HS256 tokens straddling the EdDSA length")
	}
	for _, token := range []string{below, above} {
		if alg := runJwtHelper(t, "jwt_alg '"+token+"'"); alg != "HS256" {
			t.Fatalf("the padded token must still be HS256, got %q", alg)
		}
	}
	// Both tokens sit on opposite sides of 450: no length-only predicate can
	// reject one without rejecting the other, yet both are symmetric-signed.
	if len(below) >= eddsaTokenLen || len(above) <= eddsaTokenLen {
		t.Fatalf("expected lengths to straddle %d, got %d and %d",
			eddsaTokenLen, len(below), len(above))
	}

	// The two candidate assertion forms, evaluated on the same HS256 token.
	// The weakened form (length only) records a pass — it is exactly the
	// mutation the smoke must be immune to; the real form fails it.
	weakOut := runJwtHelper(t, "TOKEN='"+above+"'\n"+
		`if [ ${#TOKEN} -ge 400 ]; then printf pass; else printf fail; fi`)
	if weakOut != "pass" {
		t.Fatalf("the weakened (length-only) assertion must accept an HS256 token, got %q", weakOut)
	}
	realOut := runJwtHelper(t, "TOKEN='"+above+"'\n"+
		`if [ "$(jwt_alg "$TOKEN")" = "EdDSA" ]; then printf pass; else printf fail; fi`)
	if realOut != "fail" {
		t.Fatalf("the real (alg) assertion must reject an HS256 token, got %q", realOut)
	}
}

// TestKustomizeBuildJwtSecretAndNoPostgresPVC covers AC-065-29 + AC-065-01 at
// the manifest level with the real kustomize: the dev overlay materializes the
// two JWT Secrets from the local key pair (private -> auth only, public -> the
// verifiers cmd/orchestrator and cmd/api) and no longer declares a PostgreSQL
// PVC. Skipped when kustomize is not on PATH.
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
	// ../../../data/dev-jwt/jwt-{private,public}-key.pem relative to
	// deploy/kustomize/dev resolves inside tmp. A symlink does NOT work:
	// kustomize resolves the kustomization dir through symlinks and then
	// resolves relative file sources from the REAL location — the key path
	// would land back in the repo data dir (the destructive bug this test
	// isolation fixes). Copy the tree instead.
	copyTree(t, filepath.Join(root, "deploy", "kustomize"), filepath.Join(tmp, "deploy", "kustomize"))
	kustomizePrivateKey := testJWTPrivateKeyPEM(t)
	writeDevJWTKeyFiles(t, filepath.Join(tmp, "data"), kustomizePrivateKey, testPublicKeyPEM(t, kustomizePrivateKey))
	// AC-065-33: the webhook-service-token secretGenerator sources the same
	// data-dir layout; provide the file in the isolated tree.
	tokenPath := filepath.Join(tmp, "data", "dev-service-tokens", "webhook-service-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("test-service-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"ci-api-key": "ci-api-key-value", "harbor-service-token": "harbor-service-token-value", "notifier-service-token": "notifier-service-token-value"} {
		extra := filepath.Join(filepath.Dir(tokenPath), name)
		if err := os.WriteFile(extra, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
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
	// kustomize appends a content hash to each generated Secret name and rewrites
	// every reference: assert both Secrets and their data keys.
	if !strings.Contains(manifest, "name: release-manager-jwt-private-") {
		t.Fatalf("expected generated release-manager-jwt-private-<hash> Secret in built manifest:\n%s", manifest)
	}
	if !strings.Contains(manifest, "name: release-manager-jwt-public-") {
		t.Fatalf("expected generated release-manager-jwt-public-<hash> Secret in built manifest:\n%s", manifest)
	}
	if !strings.Contains(manifest, "JWT_PRIVATE_KEY:") {
		t.Fatalf("expected JWT_PRIVATE_KEY data key in built manifest:\n%s", manifest)
	}
	if !strings.Contains(manifest, "JWT_PUBLIC_KEY:") {
		t.Fatalf("expected JWT_PUBLIC_KEY data key in built manifest:\n%s", manifest)
	}
	// REQ-065 AC-065-01 least privilege, asserted on the built manifest: the
	// signing key reaches ONLY cmd/auth, the public key ONLY cmd/orchestrator,
	// and webhook/notifier mount neither (they never verified tokens).
	docs := strings.Split(manifest, "\n---\n")
	docFor := func(name string) string {
		for _, doc := range docs {
			if strings.Contains(doc, "name: "+name+"\n") && strings.Contains(doc, "kind: Deployment") {
				return doc
			}
		}
		return ""
	}
	// Both mount shapes are checked: an env secretKeyRef carries the data key
	// name (JWT_PRIVATE_KEY/JWT_PUBLIC_KEY) while an envFrom secretRef only
	// carries the Secret name (release-manager-jwt-*), so a name-only assertion
	// would miss a whole-Secret mount.
	if authDoc := docFor("auth"); !strings.Contains(authDoc, "JWT_PRIVATE_KEY") ||
		strings.Contains(authDoc, "JWT_PUBLIC_KEY") || strings.Contains(authDoc, "release-manager-jwt-public") {
		t.Fatalf("auth Deployment must mount only the private key:\n%s", authDoc)
	}
	if orchDoc := docFor("orchestrator"); !strings.Contains(orchDoc, "JWT_PUBLIC_KEY") ||
		strings.Contains(orchDoc, "JWT_PRIVATE_KEY") || strings.Contains(orchDoc, "release-manager-jwt-private") {
		t.Fatalf("orchestrator Deployment must mount only the public key:\n%s", orchDoc)
	}
	// cmd/api is the second management-plane verifier (REQ-065 D2): the dev
	// environment deploys it so its Ed25519 public-key verification is covered
	// by the e2e smoke, and it must hold the public half only.
	if apiDoc := docFor("api"); !strings.Contains(apiDoc, "JWT_PUBLIC_KEY") ||
		strings.Contains(apiDoc, "JWT_PRIVATE_KEY") || strings.Contains(apiDoc, "release-manager-jwt-private") {
		t.Fatalf("api Deployment must mount only the public key:\n%s", apiDoc)
	}
	for _, name := range []string{"webhook", "notifier"} {
		doc := docFor(name)
		if strings.Contains(doc, "JWT_") || strings.Contains(doc, "release-manager-jwt") {
			t.Fatalf("%s Deployment must not mount any JWT key (exposure with zero benefit):\n%s", name, doc)
		}
	}
	// The release-api Service must publish the port the dev host band maps to
	// it: NodePort 30088 (host 8088), the only free entry above the six
	// original services (8087 is web). docs/testing.md states the same number.
	if !strings.Contains(manifest, "nodePort: 30088") {
		t.Fatalf("api Service must expose nodePort 30088:\n%s", manifest)
	}
	if !strings.Contains(manifest, "name: api-config-") {
		t.Fatalf("expected generated api-config-<hash> ConfigMap in built manifest:\n%s", manifest)
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
	env = append(env, "DEV_PROFILE=ci", "E2E_RUN_ID=ci-run-003", "DEV_JWT_PRIVATE_KEY="+testJWTPrivateKeyPEM(t),
		"DEV_WEBHOOK_SERVICE_TOKEN=ci-service-token", "DEV_CI_API_KEY=ci-api-key-value", "DEV_HARBOR_SERVICE_TOKEN=ci-harbor-token", "DEV_NOTIFIER_SERVICE_TOKEN=ci-notifier-token", "DEV_M_TLS_CA_KEY=ci-ca-key", "DEV_M_TLS_CA_CERT=ci-ca-cert")

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

// TestOrchestratorDevConfigWiresTopLevelCA locks the AC-065-36 injection
// contract at the config seam (real smoke 2026-08-28 independent-audit
// failure): merge 40d0a69 adopted PR#88's top-level CAConfig
// (ca.key_path/ca.cert_path, ADR-017) as the operator CA contract, but the
// dev overlay still carried the legacy gateway.ca_key_path/ca_cert_path keys
// (GatewayCfg dead fields with zero consumers) — ca.LoadConfigured(s.cfg.CA)
// then failed `ca_invalid` and the orchestrator CrashLoopBackOff'd before
// readiness. Tests that construct config programmatically cannot see this
// drift; this test decodes the exact YAML kustomize ships with the
// production loader and asserts the wiring.
func TestOrchestratorDevConfigWiresTopLevelCA(t *testing.T) {
	overlay := filepath.Join(repoRoot(t), "deploy", "kustomize", "dev", "configs", "orchestrator.dev.yaml")
	cfg, err := config.LoadService(overlay)
	if err != nil {
		t.Fatalf("loading dev orchestrator config: %v", err)
	}
	if cfg.CA.KeyPath != "/data/gateway-ca.key" {
		t.Fatalf("expected ca.key_path=/data/gateway-ca.key (release-manager-mtls-ca Secret subPath), got %q", cfg.CA.KeyPath)
	}
	if cfg.CA.CertPath != "/data/gateway-ca.crt" {
		t.Fatalf("expected ca.cert_path=/data/gateway-ca.crt (release-manager-mtls-ca Secret subPath), got %q", cfg.CA.CertPath)
	}
	// ca.LoadConfigured fail-closes on Validate; the dev overlay must decode
	// to a CA config that passes exactly the validation the real orchestrator
	// startup runs (cmd/orchestrator main.go LoadConfigured).
	if err := cfg.CA.Validate(); err != nil {
		t.Fatalf("dev orchestrator CA config must validate: %v", err)
	}
	// Anti-drift: the legacy gateway.ca_key_path/ca_cert_path dead fields had
	// zero consumers and were removed from GatewayCfg (TASK-094 §7-4). The raw
	// dev overlay must not reintroduce keys nothing decodes — that is the
	// exact trap that caused the 2026-08-28 audit failure (they silently
	// shadow the top-level ca: contract). Decoding the gateway block into a
	// plain map keeps this assertion independent of the struct that no longer
	// carries the fields.
	raw := struct {
		Gateway map[string]any `yaml:"gateway"`
	}{}
	overlayBytes, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatalf("reading dev orchestrator overlay: %v", err)
	}
	if err := yaml.Unmarshal(overlayBytes, &raw); err != nil {
		t.Fatalf("decoding dev orchestrator overlay: %v", err)
	}
	for _, deadKey := range []string{"ca_key_path", "ca_cert_path"} {
		if _, present := raw.Gateway[deadKey]; present {
			t.Fatalf("legacy gateway.%s dead key must not return to the dev overlay (GatewayCfg no longer decodes it)", deadKey)
		}
	}
}

// TestCustomerAgentConfigCarriesCustomerUUIDs locks the operator enrollment
// contract (real smoke 2026-08-27: `customer_id mismatch` — the agent config
// carried the customer NAME while devseed enrollment tokens carry the
// deterministic customer UUID): each customer-agent overlay's operator config
// pairs its cluster id with the fixture customer UUID the seed resolves.
func TestCustomerAgentConfigCarriesCustomerUUIDs(t *testing.T) {
	kustomize, err := exec.LookPath("kustomize")
	if err != nil {
		t.Skip("kustomize not installed")
	}
	root := repoRoot(t)
	tmp := t.TempDir()
	copyTree(t, filepath.Join(root, "deploy", "kustomize"), filepath.Join(tmp, "deploy", "kustomize"))
	want := map[string]string{
		"dev-customer-a-direct":     "11111111-1111-4111-8111-111111111111",
		"dev-customer-a-cache":      "11111111-1111-4111-8111-111111111111",
		"dev-customer-b-replicated": "22222222-2222-4222-8222-222222222222",
		"dev-customer-b-mixed":      "22222222-2222-4222-8222-222222222222",
	}
	overlays := map[string]string{
		"dev-customer-a-direct":     "c1-direct",
		"dev-customer-a-cache":      "c2-cache",
		"dev-customer-b-replicated": "c3-replicated",
		"dev-customer-b-mixed":      "c4-mixed",
	}
	for cluster, overlay := range overlays {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, kustomize, "build", filepath.Join(tmp, "deploy", "kustomize", "customer-agent", overlay)).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("kustomize build %s failed: %v\n%s", overlay, err, out)
		}
		manifest := string(out)
		if !strings.Contains(manifest, "customer_id: "+want[cluster]) {
			t.Fatalf("operator config for %s must carry customer UUID %s:\n%s", cluster, want[cluster], manifest)
		}
		if !strings.Contains(manifest, "cluster_id: "+cluster) {
			t.Fatalf("operator config for %s must carry its own cluster id:\n%s", cluster, manifest)
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

	// Seed-state files: dev-down wipes the emptyDir PostgreSQL state, so the
	// persisted seed progress/manifest must go too (real smoke 2026-08-28
	// independent audit: dev-down→dev-up fixture_conflict identity drift
	// when all-committed progress replays against an empty database). The
	// single-use enrollment tokens are equally cluster-bound: stale token
	// files make the next dev-up skip agents_up and the install phase fails
	// with stage_unavailable (no operator for cluster — same recovery-path
	// defect, observed in the same real smoke).
	for _, file := range []string{"dev-seed-progress.json", "dev-fixture.json"} {
		if err := os.WriteFile(filepath.Join(stateDir, file), []byte(`{"stale":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tokenPath := filepath.Join(stateDir, "dev-enrollment-tokens", "dev-customer-a-direct.token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("stale-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"ci-api-key": "ci-api-key-value", "harbor-service-token": "harbor-service-token-value", "notifier-service-token": "notifier-service-token-value"} {
		extra := filepath.Join(filepath.Dir(tokenPath), name)
		if err := os.WriteFile(extra, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := runDev(t, env, "down"); err != nil {
		t.Fatalf("dev-down failed:\n%s", out)
	}
	for _, file := range []string{"dev-seed-progress.json", "dev-fixture.json"} {
		if _, err := os.Stat(filepath.Join(stateDir, file)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected %s removed by dev-down (cluster-side seed state gone, stat err=%v)", file, err)
		}
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected enrollment tokens removed by dev-down (single-use, cluster-bound, stat err=%v)", err)
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
	// precedes the network removal line; the management node (bridged into
	// each customer network by mgmt_node_connect) is disconnected too.
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
		// The management node is bridged into every customer network
		// (mgmt_node_connect) — teardown must disconnect it before the rm.
		for _, network := range []string{
			"k3d-dev-customer-a-direct", "k3d-dev-customer-a-cache",
			"k3d-dev-customer-b-replicated", "k3d-dev-customer-b-mixed",
		} {
			mDisconnect := bytes.Index(logged, []byte("disconnect -f "+network+" k3d-release-manager-control-server-0"))
			mRm := bytes.Index(logged, []byte("rm "+network))
			if mDisconnect == -1 {
				t.Fatalf("expected management node disconnect for %s:\n%s", network, logged)
			}
			if mDisconnect > mRm {
				t.Fatalf("D-017 order violated for %s: management disconnect must precede rm:\n%s", network, logged)
			}
		}
	}
}

// TestDevUpBridgesManagementNodeIntoCustlerNetworks locks the customer-agent
// reachability contract (real smoke 2026-08-27: the control node's IP is not
// routable from customer pods, so agents can never reach the operator
// gateway): dev-up bridges the management node into every customer cluster's
// network (mgmt_node_connect) and never into its own network. The docker
// shim answers network inspect statefully via the append-only k3d create log
// (a network exists once its cluster was created — mirroring the real k3d
// leftover when the registry holds the network).
func TestDevUpBridgesManagementNodeIntoCustlerNetworks(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// Override the happy docker shim: record network verbs and answer
	// network inspect statefully (present once the cluster exists in the
	// append-only create log), so the conflict gate sees absent before
	// create while mgmt_node_connect sees present after.
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "network" ]; then
  if [ "$2" = "inspect" ]; then
    cluster="${3#k3d-}"
    if grep -q "^${cluster}|" "$DEV_DATA_DIR/k3d-creates.log" 2>/dev/null; then exit 0; fi
    exit 1
  fi
  printf '%s\n' "$*" >> "$DEV_DATA_DIR/docker-net.log"
  exit 0
fi
if [ "$1" = "container" ] && [ "$2" = "create" ]; then
  printf '%s\n' "$*" >> "$DEV_DATA_DIR/docker-create.log"
  exit 0
fi
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
exit 0
`)

	if out, err := runDev(t, env, "up"); err != nil {
		t.Fatalf("dev-up failed:\n%s", out)
	}
	logged, err := os.ReadFile(filepath.Join(stateDir, "docker-net.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cluster := range []string{
		"dev-customer-a-direct", "dev-customer-a-cache", "dev-customer-b-replicated", "dev-customer-b-mixed",
	} {
		want := "network connect k3d-" + cluster + " k3d-release-manager-control-server-0"
		if !bytes.Contains(logged, []byte(want)) {
			t.Fatalf("expected management node bridge for %s:\nlog:\n%s", cluster, logged)
		}
	}
	if bytes.Contains(logged, []byte("connect k3d-release-manager-control ")) {
		t.Fatalf("management node must not be bridged into its own network:\n%s", logged)
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
if [ "$1" = "container" ] && [ "$2" = "inspect" ] && [ "$3" = "--format" ] && [[ "$4" == *NetworkSettings.Networks* ]]; then
  case "$5" in
    k3d-release-manager-control-server-0) printf '172.18.0.2\n'; exit 0 ;;
    k3d-release-manager-registry) printf '172.18.0.3\n'; exit 0 ;;
  esac
  exit 1
fi
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
		// 9 images: webhook/orchestrator/operator/auth/notifier/api/web +
		// fixture + notification-sink.
		if got := strings.Count(order, "B"); got != 9 {
			t.Fatalf("expected 9 builds, got %d:\n%s", got, order)
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
		// 9 images: webhook/orchestrator/operator/auth/notifier/api/web +
		// fixture + notification-sink.
		if got := strings.Count(order, "B"); got != 9 {
			t.Fatalf("expected 9 builds, got %d:\n%s", got, order)
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

// TestReadinessFailureNamesTheFaultingPodAndLogs covers AC-065-10: when the
// management-plane rollout does not converge, stderr must carry the faulting
// Pod name and its recent log lines, not just "rollout did not converge" (the
// cause used to be recoverable only from data/diagnostics/, which a purged CI
// environment no longer has).
//
// The fixture also pins the Secret boundary: a credential-looking line is
// redacted before it reaches stderr.
func TestReadinessFailureNamesTheFaultingPodAndLogs(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)

	// Only the rollout gate fails; the Pod probe then reports one not-Ready
	// Pod (auth-abc123) and one Ready Pod (web-def456) so the test also pins
	// that Ready Pods are not reported.
	writeShim(t, binDir, "kubectl", `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$DEV_DATA_DIR/kubectl.log"
case "$*" in
  *"rollout status"*) exit 1 ;;
  *"jsonpath="*) printf 'auth-abc123 False\nweb-def456 True\n'; exit 0 ;;
  *"logs pod/auth-abc123"*) printf 'starting auth\nbearer eyJhbGciOiJIUzI1NiJ9.leaktoken\n'; exit 0 ;;
  *"logs pod/web-def456"*) printf 'web is fine\n'; exit 0 ;;
esac
for a in "$@"; do
  if [ "$a" = "port-forward" ]; then printf 'Forwarding from 127.0.0.1:18088 -> 8088\n'; sleep 30; exit 0; fi
  if [ "$a" = "redis-cli" ]; then printf 'PONG\n'; exit 0; fi
done
exit 0
`)
	env = append(env, "DEV_TIMEOUT_READY=1")

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("expected a readiness failure, got success:\n%s", out)
	}
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("expected exit 1, got %d:\n%s", code, out)
	}
	for _, want := range []string{
		"service_unhealthy",
		"management-plane rollout did not converge",
		"auth-abc123",
		"starting auth",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("AC-065-10: stderr must contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "web-def456") {
		t.Fatalf("only not-Ready Pods may be reported:\n%s", out)
	}
	if strings.Contains(out, "eyJhbGciOiJIUzI1NiJ9") {
		t.Fatalf("AC-065-10/Secret boundary: a credential-looking log line must be redacted:\n%s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected the redaction marker in the diagnostic:\n%s", out)
	}
}

// TestDiagnosticsCollectedBeforeKubeconfig covers the AC-065-39 pre-kubeconfig
// leg (real smoke 2026-08-28): the most common fail-fast paths (host memory /
// port / registry / cluster failures) run BEFORE kustomize apply, so
// data/kubeconfig.yaml does not exist. Diagnostics must still collect the
// shell-level host context (ownership / k3d / docker) instead of returning
// zero evidence.
func TestDiagnosticsCollectedBeforeKubeconfig(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// Fail on docker build (registry_up succeeds, but no kustomize apply, so
	// no merged kubeconfig is written).
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "build" ]; then exit 1; fi
exit 0
`)

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("expected docker_build_failed, got success:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "kubeconfig.yaml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no merged kubeconfig at pre-kubeconfig failure point (stat err=%v)", statErr)
	}
	diagDir := filepath.Join(stateDir, "diagnostics")
	entries, err := os.ReadDir(diagDir)
	if err != nil {
		t.Fatalf("expected diagnostics dir after pre-kubeconfig failure: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one ISO8601 diagnostics snapshot, got %d", len(entries))
	}
	snapshot := filepath.Join(diagDir, entries[0].Name())
	hostCtx, err := os.ReadFile(filepath.Join(snapshot, "host-context.txt"))
	if err != nil {
		t.Fatalf("expected host-context.txt even without a kubeconfig, got %v", err)
	}
	if !strings.Contains(string(hostCtx), "ownership manifest") {
		t.Fatalf("host-context must include the ownership manifest:\n%s", hostCtx)
	}
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

// AC-065-14: the docker gate must name the daemon-unreachable case, not fall
// through to a later stage or a generic failure.
func TestDevUpFailsWhenDockerDaemonIsUnreachable(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	writeShim(t, binDir, "flock", "#!/usr/bin/env bash\nexit 0\n")
	// docker is on PATH but `docker info` fails: the daemon is not running.
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
for a in "$@"; do
  if [ "$a" = "info" ]; then exit 1; fi
done
exit 0
`)
	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("dev-up must fail when the docker daemon is unreachable:\n%s", out)
	}
	if !strings.Contains(out, "docker_unavailable") {
		t.Fatalf("AC-065-14: expected docker_unavailable, got:\n%s", out)
	}
}

// AC-065-21: the disk gate must fire with its own code and report the shortfall.
func TestDevUpFailsWhenDiskIsBelowTheFloor(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	writeShim(t, binDir, "flock", "#!/usr/bin/env bash\nexit 0\n")
	writeShim(t, binDir, "docker", "#!/usr/bin/env bash\nexit 0\n")
	fakeK3d(t, binDir, stateDir)
	// 1 GiB free, far below the 20 GiB floor.
	writeShim(t, binDir, "df", `#!/usr/bin/env bash
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/fake 100000 99000 1048576 99%% /\n'
`)
	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("dev-up must fail when free disk is below the floor:\n%s", out)
	}
	if !strings.Contains(out, "host_disk_insufficient") {
		t.Fatalf("AC-065-21: expected host_disk_insufficient, got:\n%s", out)
	}
}

// AC-065-07: the port gate must name the conflict. port_in_use probes with the
// bash /dev/tcp builtin, which cannot be shimmed, so the fixture occupies the
// port for real and the gate has to notice.
func TestDevUpFailsWhenADevPortIsOccupied(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)

	// fakeEnv overrides the dev ports to 19082-19087 so a real dev environment
	// can run alongside the test.
	listener, listenErr := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:19082")
	if listenErr != nil {
		t.Skipf("port 19082 is not free on this host, cannot build the fixture: %v", listenErr)
	}
	defer func() { _ = listener.Close() }()

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("dev-up must fail when a dev port is occupied:\n%s", out)
	}
	if !strings.Contains(out, "port_conflict") {
		t.Fatalf("AC-065-07: expected port_conflict, got:\n%s", out)
	}
}

// AC-065-09: the registry readiness probe must fail with its own code, not fall
// through to a later stage.
func TestDevUpFailsWhenRegistryIsUnreachable(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// The registry probe is the only curl that hits /v2/.
	writeShim(t, binDir, "curl", `#!/usr/bin/env bash
if [[ "$*" == *"/v2/"* ]]; then exit 7; fi
if [[ "$*" == *"/version"* ]]; then printf '{"version":"fixture-v2"}\n'; exit 0; fi
exit 0
`)

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("dev-up must fail when the registry is unreachable:\n%s", out)
	}
	if !strings.Contains(out, "registry_unreachable") {
		t.Fatalf("AC-065-09: expected registry_unreachable, got:\n%s", out)
	}
}

// AC-065-17: a failing k3d create must surface cluster_create_failed.
func TestDevUpFailsWhenClusterCreateFails(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// Keep the fake-k3d behaviour for reads, but make creation fail.
	writeShim(t, binDir, "k3d", `#!/usr/bin/env bash
# Answer the version probe so require_k3d passes; fail only on creation.
if [ "$1" = "version" ]; then printf 'k3d version v5.8.3\nk3s version v1.31.5-k3s1\n'; exit 0; fi
for a in "$@"; do
  if [ "$a" = "create" ]; then printf 'k3d: failed to create cluster\n' >&2; exit 1; fi
done
exit 0
`)

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("dev-up must fail when cluster creation fails:\n%s", out)
	}
	if !strings.Contains(out, "cluster_create_failed") {
		t.Fatalf("AC-065-17: expected cluster_create_failed, got:\n%s", out)
	}
}

// AC-065-16: a failing kustomize build must surface kustomize_build_failed.
func TestDevUpFailsWhenKustomizeBuildFails(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nprintf 'kustomize: no such file\\n' >&2\nexit 1\n")

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("dev-up must fail when kustomize build fails:\n%s", out)
	}
	if !strings.Contains(out, "kustomize_build_failed") {
		t.Fatalf("AC-065-16: expected kustomize_build_failed, got:\n%s", out)
	}
}

// AC-065-19: a failing seed leg must surface seed_write_failed with the
// devseed output, so the failing stage is visible.
func TestDevUpFailsWhenSeedLegFails(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// devseed runs through `go run ./cmd/devseed`; fail only that invocation so
	// the earlier `go env` probe still passes.
	// The seed leg is the devseed invocation carrying --seed-retries. The shim keeps
	// happyShims' behaviour (mTLS CA files, service tokens) for every other call,
	// because the CA helper runs the same binary earlier.
	writeShim(t, binDir, "go", `#!/usr/bin/env bash
for a in "$@"; do
  if [ "$a" = "--seed-retries" ]; then printf 'devseed: write failed\n' >&2; exit 1; fi
done
#!/usr/bin/env bash
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

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("dev-up must fail when the seed leg fails:\n%s", out)
	}
	if !strings.Contains(out, "seed_write_failed") {
		t.Fatalf("AC-065-19: expected seed_write_failed, got:\n%s", out)
	}
}

// AC-065-35: the SPA fallback must not swallow the orchestrator's health and
// environment endpoints. The three proxy locations use `^~` so prefix matching
// wins over `location /`, and nginx.conf is what the web image installs.
func TestNginxConfProxiesHealthAndEnvironmentBeforeSPAFallback(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "web", "nginx.conf"))
	if err != nil {
		t.Fatal(err)
	}
	conf := string(raw)

	fallback := strings.Index(conf, "location / {")
	if fallback < 0 {
		t.Fatalf("nginx.conf must keep the SPA fallback location /")
	}
	for _, path := range []string{"/health", "/readyz", "/environment"} {
		marker := "location ^~ " + path + " {"
		at := strings.Index(conf, marker)
		if at < 0 {
			t.Fatalf("AC-065-35: nginx.conf must proxy %s with ^~ so it beats the SPA fallback", path)
		}
		if at > fallback {
			t.Fatalf("AC-065-35: %s must be declared before location / or the fallback wins", path)
		}
		block := conf[at:]
		if end := strings.Index(block, "}"); end > 0 {
			block = block[:end]
		}
		if !strings.Contains(block, "proxy_pass http://orchestrator:8083;") {
			t.Fatalf("AC-065-35: %s must proxy to the orchestrator, got:\n%s", path, block)
		}
	}
}

// AC-065-15: when every build succeeds and a push fails, the failure class must
// be docker_push_failed and must name the image.
func TestDevUpFailsWithPushFailureCode(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// A missing manifest forces the build path; builds succeed, release pushes
	// fail (the k3s component prewarm pushes still pass).
	writeShim(t, binDir, "docker", `#!/usr/bin/env bash
if [ "$1" = "manifest" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then exit 1; fi
if [ "$1" = "build" ]; then exit 0; fi
if [ "$1" = "push" ]; then
  if [[ "$*" == *"release-"* ]]; then printf 'push denied\n' >&2; exit 1; fi
  exit 0
fi
exit 0
`)

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("dev-up must fail when an image push fails:\n%s", out)
	}
	if !strings.Contains(out, "docker_push_failed") {
		t.Fatalf("AC-065-15: expected docker_push_failed, got:\n%s", out)
	}
	if strings.Contains(out, "docker_build_failed") {
		t.Fatalf("AC-065-15: builds succeeded, so the class must not be a build failure:\n%s", out)
	}
	if !strings.Contains(out, "release-") {
		t.Fatalf("AC-065-15: the failing image must be named:\n%s", out)
	}
}

// AC-065-31: a local-profile failure must KEEP the partial managed resources so
// the next dev-up can converge, and that rerun must not recreate them.
func TestLocalFailureKeepsPartialResourcesAndResumes(t *testing.T) {
	stateDir := t.TempDir()
	env, binDir := fakeEnv(t, stateDir)
	fakeK3d(t, binDir, stateDir)
	happyShims(t, binDir)
	// Fail after the clusters exist: kustomize is stage 5, clusters are stage 3.
	writeShim(t, binDir, "kustomize", "#!/usr/bin/env bash\nexit 1\n")

	out, err := runDev(t, env, "up")
	if err == nil {
		t.Fatalf("dev-up must fail when kustomize build fails:\n%s", out)
	}

	clustersPath := filepath.Join(stateDir, "clusters.txt")
	createsPath := filepath.Join(stateDir, "k3d-creates.log")
	clustersAfterFailure, readErr := os.ReadFile(clustersPath)
	if readErr != nil {
		t.Fatalf("the fixture must have created clusters before the failure: %v", readErr)
	}
	created := strings.Count(string(clustersAfterFailure), "\n")
	if created == 0 {
		t.Fatalf("fixture created no clusters, the test would prove nothing")
	}
	firstCreates, readErr := os.ReadFile(createsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}

	// The local profile must not auto-clean on failure (D6=A): the clusters are
	// still recorded.
	stillThere, readErr := os.ReadFile(clustersPath)
	if readErr != nil {
		t.Fatalf("AC-065-31: a local failure must keep the partial resources: %v", readErr)
	}
	if got := strings.Count(string(stillThere), "\n"); got != created {
		t.Fatalf("AC-065-31: expected %d clusters retained, got %d:\n%s", created, got, stillThere)
	}

	// A rerun converges: the same clusters are reused rather than recreated.
	if _, err := runDev(t, env, "up"); err == nil {
		t.Fatalf("dev-up should still fail while kustomize is broken")
	}
	secondCreates, readErr := os.ReadFile(createsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(secondCreates, firstCreates) {
		t.Fatalf("AC-065-31: the rerun must resume, not recreate clusters.\nfirst:\n%s\nsecond:\n%s",
			firstCreates, secondCreates)
	}
}

// runMake runs a Makefile target from the repository root with an explicit
// environment, the same way CI does.
func runMake(t *testing.T, env []string, target string) (string, error) {
	t.Helper()
	root := repoRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "make", "--no-print-directory", target)
	cmd.Dir = root
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// e2eEnvConfigFixture writes the two inputs `make e2e-env-config` assembles
// from, into an isolated data directory. It never touches the repository's own
// data/ directory.
func e2eEnvConfigFixture(t *testing.T, dir string) {
	t.Helper()
	fixture := `{
  "customers": {"customer-a": {"id": "cust-1"}, "customer-b": {"id": "cust-2"}},
  "clusters": {"release-manager-control": {}, "dev-customer-a-direct": {}, "dev-customer-b-replicated": {}},
  "routes": {"route-1": {}, "route-2": {}, "route-3": {}},
  "bundle": {"id": "bundle-1"},
  "operators": {"op-a": {"id": "operator-1"}},
  "definitions": {
    "app-basic": {"id": "def-basic", "bundle_id": "bundle-1", "values_revision_id": "rev-1"},
    "e2e-release-target": {"id": "def-release", "bundle_id": "bundle-1", "values_revision_id": "rev-1"},
    "e2e-isolation-target": {"id": "def-isolation", "bundle_id": "bundle-1", "values_revision_id": "rev-1"},
    "e2e-restart-target": {"id": "def-restart", "bundle_id": "bundle-1", "values_revision_id": "rev-1"},
    "e2e-emergency-target": {"id": "def-emergency", "bundle_id": "bundle-1", "values_revision_id": "rev-1"}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "dev-fixture.json"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
}

// AC-066-29: `make e2e-env-config` assembles the runtime config from the status
// and fixture artifacts, writes it 0600, and never carries the password.
func TestE2EEnvConfigAssemblesTheManifest(t *testing.T) {
	dir := t.TempDir()
	e2eEnvConfigFixture(t, dir)
	configPath := filepath.Join(dir, "e2e-env-config.yaml")

	// AC-066-43: the manifest names this variable and never carries the secret.
	// Set it on the test process too, because LoadConfig resolves it from there
	// while the child inherits it through os.Environ below.
	t.Setenv("E2E_RUNNER_PASSWORD", "from-the-environment")

	env := append(os.Environ(),
		"DEV_DATA_DIR="+dir,
		"E2E_DATA_DIR="+dir,
		"E2E_ENV_CONFIG="+configPath,
		"E2E_RESTART_DEPLOYMENTS=release-auth release-operator-gateway release-orchestrator",
		"E2E_TEST_NAMESPACE=release-manager-dev",
	)
	if out, err := runMake(t, env, "e2e-env-config"); err != nil {
		t.Fatalf("make e2e-env-config failed: %v\n%s", err, out)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("config was not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %o, want 0600 (AC-066-29)", perm)
	}

	manifest, err := e2e.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("the assembled manifest must load: %v", err)
	}
	// expected_identity is derived from the fixture, not copied from it.
	if manifest.Seed.ExpectedIdentity.Customers != 2 {
		t.Fatalf("expected_identity.customers = %d, want 2", manifest.Seed.ExpectedIdentity.Customers)
	}
	if manifest.Seed.ExpectedIdentity.RoutesBasic != 3 {
		t.Fatalf("expected_identity.routes_basic = %d, want 3", manifest.Seed.ExpectedIdentity.RoutesBasic)
	}
	if len(manifest.Seed.ExpectedIdentity.E2EDefinitionIDs) != 4 {
		t.Fatalf("expected_identity.e2e_definition_ids = %v, want 4 entries", manifest.Seed.ExpectedIdentity.E2EDefinitionIDs)
	}
	// The emergency target is excluded from the upgrade targets.
	if len(manifest.Seed.UpgradeTargets) != 3 {
		t.Fatalf("e2e_upgrade_targets = %d entries, want 3 (emergency excluded)", len(manifest.Seed.UpgradeTargets))
	}
	// AC-066-43: the manifest names the variable, never the secret.
	if manifest.Credentials.E2ERunner.PasswordEnv != "E2E_RUNNER_PASSWORD" {
		t.Fatalf("password_env = %q", manifest.Credentials.E2ERunner.PasswordEnv)
	}
}

// AC-066-29: a fixture missing a required definition field fails the assembly
// instead of producing a manifest with an empty id.
func TestE2EEnvConfigRejectsAnIncompleteFixture(t *testing.T) {
	dir := t.TempDir()
	e2eEnvConfigFixture(t, dir)
	broken := `{"customers": {}, "clusters": {}, "operators": {}, "definitions": {}}`
	if err := os.WriteFile(filepath.Join(dir, "dev-fixture.json"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "e2e-env-config.yaml")

	env := append(os.Environ(),
		"DEV_DATA_DIR="+dir,
		"E2E_DATA_DIR="+dir,
		"E2E_ENV_CONFIG="+configPath,
		"E2E_RESTART_DEPLOYMENTS=release-auth release-operator-gateway release-orchestrator",
	)
	if out, err := runMake(t, env, "e2e-env-config"); err == nil {
		t.Fatalf("an incomplete fixture must fail the assembly:\n%s", out)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no config may be written when the fixture is incomplete, stat err=%v", err)
	}
}

// AC-066-43: e2e-stage and e2e-cleanup fail closed when the runner password is
// neither in the environment nor in the credentials file. The guard runs before
// anything is launched, so this is assertable without a cluster.
func TestE2EStageFailsClosedWithoutTheRunnerPassword(t *testing.T) {
	for _, target := range []string{"e2e-stage", "e2e-cleanup"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			e2eEnvConfigFixture(t, dir)
			// Point the credentials file at a path that does not exist, so the
			// only possible source of the password is the environment.
			env := append(os.Environ(),
				"DEV_DATA_DIR="+dir,
				"E2E_DATA_DIR="+dir,
				"E2E_CREDENTIALS_FILE="+filepath.Join(dir, "absent-credentials.env"),
				"E2E_ENV_CONFIG="+filepath.Join(dir, "e2e-env-config.yaml"),
				"E2E_RUNNER_PASSWORD=",
			)
			out, err := runMake(t, env, target)
			if err == nil {
				t.Fatalf("%s must fail closed without the runner password:\n%s", target, out)
			}
			if !strings.Contains(out, "E2E_RUNNER_PASSWORD must be set") {
				t.Fatalf("%s must report the documented guard, got:\n%s", target, out)
			}
		})
	}
}
