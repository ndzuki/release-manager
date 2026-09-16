package devtest

// Kustomize probe-truthfulness gate (REQ-099 AC2, TASK-099 AC2).
//
// "Pod Ready" must mean something: every Deployment container in
// deploy/kustomize carries a startupProbe that absorbs the boot/migration
// window, explicit timeoutSeconds on every probe (implicit 1s silently
// killed slow probes under load), and the readiness/liveness pair is split
// (/readyz vs /health) — never two names for one semantics and never an
// unconditional liveness endpoint as the readiness signal. The historical
// state (0 startupProbe hits, 0 explicit timeouts across the tree) is what
// this gate prevents from regressing.
//
// Webhook/notification-sink/operator-agent additionally gate /readyz on real
// preconditions in Go (ReadinessChecks); this file only pins the manifest
// shape, unit tests pin the check semantics.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// kustomizeRoot is the directory whose trees this gate walks; fixtures
// (deploy/fixtures/chart) are release PAYLOADS rendered into customer
// clusters by the Helm engine, not release-manager runtime, and are
// deliberately out of scope.
const kustomizeRoot = "deploy/kustomize"

// probePair documents the per-deployment expectation for httpGet liveness:
// every Go service pairs /health (liveness) with /readyz (readiness). web is
// a static asset server: its only endpoint is /, so readiness/liveness share
// the path and the startupProbe carries the boot semantics. Any new httpGet
// service must pick the pair explicitly here.
var livenessToReadiness = map[string]string{
	"/health": "/readyz",
	"/":       "/",
}

func TestKustomizeProbesAreTruthful(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", kustomizeRoot))
	// t.TempDir runs relative to the package dir; resolve from the repo root
	// so the walk is reproducible from either cwd.
	if _, err := os.Stat(root); err != nil {
		root = filepath.Join("..", "..", kustomizeRoot)
	}
	files := deploymentFiles(t, root)
	require.GreaterOrEqual(t, len(files), 9, "expected every runtime Deployment under %s", kustomizeRoot)

	var checked int
	for _, file := range files {
		file := file
		t.Run(strings.TrimPrefix(filepath.ToSlash(file), "../"), func(t *testing.T) {
			data, err := os.ReadFile(file)
			require.NoError(t, err)
			for _, doc := range strings.Split(string(data), "\n---\n") {
				var obj map[string]any
				if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj == nil {
					continue
				}
				if obj["kind"] != "Deployment" {
					continue
				}
				problems := checkDeploymentProbes(obj)
				sort.Strings(problems)
				require.Emptyf(t, problems, "probe gate failures in %s:\n  %s", file, strings.Join(problems, "\n  "))
				checked++
			}
		})
	}
	require.GreaterOrEqual(t, checked, 9, "gate must cover every runtime Deployment, found %d", checked)
}

// TestProbeGateRejectsHistoricalShape is the negative control (TASK-099: the
// gate must be able to fail): the pre-REQ-099 auth probe block — no
// startupProbe, no explicit timeouts, liveness path as readiness — must be
// reported with all three violations.
func TestProbeGateRejectsHistoricalShape(t *testing.T) {
	const historical = `
kind: Deployment
metadata:
  name: auth
spec:
  template:
    spec:
      containers:
        - name: auth
          readinessProbe:
            httpGet:
              path: /health
              port: http
            periodSeconds: 5
          livenessProbe:
            httpGet:
              path: /health
              port: http
            initialDelaySeconds: 10
            periodSeconds: 10
`
	var obj map[string]any
	// Build the same document as YAML data to exercise the real parser path.
	require.NoError(t, yaml.Unmarshal([]byte(historical), &obj))
	problems := checkDeploymentProbes(obj)
	joined := strings.Join(problems, "\n")
	require.Contains(t, joined, "missing startupProbe")
	require.Contains(t, joined, "missing explicit timeoutSeconds")
	require.Contains(t, joined, "readiness must not duplicate liveness path")
}

// checkDeploymentProbes returns violations for one Deployment object.
func checkDeploymentProbes(obj map[string]any) []string {
	var problems []string
	spec := asMap(obj["spec"])
	if spec == nil {
		return []string{"no spec"}
	}
	pod := asMap(spec["template"])
	podSpec := asMap(pod["spec"])
	containers, ok := podSpec["containers"].([]any)
	if !ok || len(containers) == 0 {
		return []string{"no containers"}
	}
	for _, raw := range containers {
		c := asMap(raw)
		name, ok := c["name"].(string)
		if !ok || name == "" {
			problems = append(problems, "container without a string name")
			continue
		}
		if c["startupProbe"] == nil {
			problems = append(problems, name+": missing startupProbe (boot/migration window must not fight liveness)")
		}
		pairs := [][2]string{{"startupProbe", "startup"}, {"readinessProbe", "readiness"}, {"livenessProbe", "liveness"}}
		var readinessPath, livenessPath string
		var readinessIsHTTP, livenessIsHTTP bool
		for _, p := range pairs {
			probe := asMap(c[p[0]])
			if probe == nil {
				continue
			}
			timeout, ok := probe["timeoutSeconds"].(int)
			if !ok || timeout <= 0 {
				problems = append(problems, name+": "+p[1]+"Probe missing explicit timeoutSeconds (k8s default 1s fails under load)")
			}
			httpGet := asMap(probe["httpGet"])
			if httpGet == nil {
				continue
			}
			path, ok := httpGet["path"].(string)
			if !ok {
				problems = append(problems, name+": "+p[1]+"Probe httpGet without a string path")
				continue
			}
			switch p[1] {
			case "readiness":
				readinessPath, readinessIsHTTP = path, true
			case "liveness":
				livenessPath, livenessIsHTTP = path, true
			}
		}
		if readinessIsHTTP && livenessIsHTTP {
			if readinessPath == livenessPath && livenessPath != "/" {
				problems = append(problems, name+": readiness must not duplicate liveness path (split /readyz vs /health)")
			}
			want, ok := livenessToReadiness[livenessPath]
			if !ok {
				problems = append(problems, name+": unexpected liveness path "+livenessPath+" (register the pair in livenessToReadiness)")
			} else if readinessPath != want {
				problems = append(problems, name+": liveness "+livenessPath+" expects readiness "+want+", got "+readinessPath)
			}
		}
	}
	return problems
}

func deploymentFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "kind: Deployment") {
			out = append(out, path)
		}
		return nil
	}))
	return out
}

func asMap(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}
