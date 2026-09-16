package config_test

// Config-key truthfulness gate (REQ-094 spec 3, TASK-094 AC1/AC3).
//
// Direction A — file keys decode: every leaf key of every service config file
// (configs/*.dev.yaml and deploy/kustomize/**/configs/*.yaml) must resolve to
// a mapstructure path of the loader that actually parses the file
// (config.LoadService -> ServiceConfig), or to a raw section that
// cmd/orchestrator reads through its own typed struct (gc / emergency / trust).
// An unresolvable key decodes to nothing — the drift class that produced
// §7-2/§7-3/§7-5.
//
// Direction B — struct fields have readers: every leaf mapstructure path of
// ServiceConfig must correspond to a Go field whose selector name appears in
// production code (cmd/, internal/, non-test, non-gen). This is the class of
// §7-1 (log_level parsed but never read). The reader scan is name-based and
// therefore a floor, not a proof of behavior; behavior is pinned by the
// per-key tests (e.g. TestApplyLogLevelChangesHandlerLevel,
// TestCustomerAgentOverlaysWirePlainHTTPRegistry).
//
// Known exclusions (with reasons, see reasonOf):
//   - configs/e2e.dev.yaml: not a ServiceConfig file; consumed by cmd/e2e
//     through a yaml.KnownFields(true) strict decoder that already fails on
//     unknown keys.
//   - upgrade-sdk.quarantine.yaml / installgate policy files: consumed by
//     their own CI tools with real readers, not service configuration.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/orchestrator"
)

// rawSectionStructs are top-level YAML sections cmd/orchestrator parses with
// its own typed structs instead of ServiceConfig (grep: UnmarshalKey in
// cmd/orchestrator/main.go). Leaves are validated against the struct tags.
func rawSectionStructs() map[string]any {
	return map[string]any{
		"gc":        orchestrator.GcConfig{},
		"emergency": config.EmergencyCfg{},
		// trustConfig is a private type in package main (cmd/orchestrator);
		// its single key is asserted here and pinned by TestTrustVerificationTimeoutReader.
		"trust": nil,
	}
}

var rawSectionLeafOverrides = map[string][]string{
	"trust": {"trust.verification_timeout"},
}

func TestConfigFilesKeysResolveToLoaderPaths(t *testing.T) {
	root := repoRoot(t)
	for _, file := range serviceConfigFiles(t, root) {
		file := file
		t.Run(strings.TrimPrefix(file, root+"/"), func(t *testing.T) {
			allowed := structLeafPaths(reflect.ValueOf(config.ServiceConfig{}))
			if strings.Contains(filepath.Base(file), "orchestrator") {
				for section, typ := range rawSectionStructs() {
					if typ != nil {
						for p := range structLeafPaths(reflect.ValueOf(typ)) {
							allowed[section+"."+p] = struct{}{}
						}
					}
					for _, p := range rawSectionLeafOverrides[section] {
						allowed[p] = struct{}{}
					}
				}
			}
			var doc map[string]any
			data, err := os.ReadFile(file)
			require.NoError(t, err)
			require.NoError(t, yaml.Unmarshal(data, &doc))
			var problems []string
			for _, leaf := range yamlLeafPaths("", doc) {
				if _, ok := allowed[leaf]; !ok {
					problems = append(problems, fmt.Sprintf(
						"key %q decodes to nothing: no ServiceConfig mapstructure path and no raw section read for this service", leaf))
				}
			}
			require.Emptyf(t, problems, "config-key gate failures in %s:\n  %s", file, strings.Join(problems, "\n  "))
		})
	}
}

// TestFakeKeyFailsTheGate is the negative control (TASK-094 AC: the gate must
// be able to fail): a key with no reader — top level and nested — must be
// reported. This mirrors the historical drift of `registry_plain_http` at the
// top level and `retention:` instead of `gc:`.
func TestFakeKeyFailsTheGate(t *testing.T) {
	allowed := structLeafPaths(reflect.ValueOf(config.ServiceConfig{}))
	allowed["gc.interval"] = struct{}{}

	var doc map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(`
http_port: 8083
log_level: warn
registry_plain_http: true
retention:
  bundle_days: 90
agent:
  mode: agent
  typo_field: 1
values:
  - 1
`), &doc))

	var problems []string
	for _, leaf := range yamlLeafPaths("", doc) {
		if _, ok := allowed[leaf]; !ok {
			problems = append(problems, leaf)
		}
	}
	sort.Strings(problems)
	require.Equal(t, []string{
		"agent.typo_field",
		"registry_plain_http",
		"retention.bundle_days",
		"values",
	}, problems, "fake keys must be reported; real keys must pass")
}

// TestStructLeavesHaveReaders is direction B: every ServiceConfig leaf field
// needs a production selector. The map names leaf path -> Go field name.
func TestStructLeavesHaveReaders(t *testing.T) {
	root := repoRoot(t)
	readers := productionSelectorNames(t, root)
	// Explicit retention allowlist (empty = nothing tolerated without a
	// reader). Add entries only with a written reason, per REQ-094.
	allowlist := map[string]string{}

	var problems []string
	for _, path := range sortedKeys(structLeafPathsWithFieldNames(reflect.ValueOf(config.ServiceConfig{}))) {
		field := structLeafPathsWithFieldNames(reflect.ValueOf(config.ServiceConfig{}))[path]
		if _, ok := readers[field]; ok {
			continue
		}
		if _, ok := allowlist[path]; ok {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s (field %s)", path, field))
	}
	require.Emptyf(t, problems,
		"ServiceConfig leaves without any production reader (dead fields — wire or delete):\n  %s",
		strings.Join(problems, "\n  "))
}

// TestTrustVerificationTimeoutReader pins the one raw leaf the gate cannot
// reflect privately: cmd/orchestrator's trustConfig.VerificationTimeout must
// keep being selected, otherwise the `trust:` section allowance here goes
// stale.
func TestTrustVerificationTimeoutReader(t *testing.T) {
	root := repoRoot(t)
	readers := productionSelectorNames(t, root)
	_, ok := readers["VerificationTimeout"]
	require.True(t, ok, "trust.verification_timeout lost its reader")
}

// TestCustomerAgentOverlaysWirePlainHTTPRegistry is the REQ-094 AC3 evidence
// for the §7-5 placement fix: config.LoadService must decode
// agent.registry_plain_http=true for all four customer-agent overlays. The
// key used to sit at the top level, where nothing decodes it, so every agent
// silently started with HTTPS-only OCI pulls despite its comment.
func TestCustomerAgentOverlaysWirePlainHTTPRegistry(t *testing.T) {
	root := repoRoot(t)
	for _, overlay := range []string{"c1-direct", "c2-cache", "c3-replicated", "c4-mixed"} {
		t.Run(overlay, func(t *testing.T) {
			cfg, err := config.LoadService(filepath.Join(root, "deploy", "kustomize",
				"customer-agent", overlay, "configs", "operator.dev.yaml"))
			require.NoError(t, err)
			require.True(t, cfg.Agent.RegistryPlainHTTP,
				"agent.registry_plain_http must decode true (placement was fixed in TASK-094 §7-5)")
			require.Equal(t, "agent", cfg.Agent.Mode)
		})
	}
}

// --- helpers ---------------------------------------------------------------

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "go.mod not found above test dir")
		dir = parent
	}
}

func serviceConfigFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	add := func(rel string) {
		if reason := reasonOf(rel); reason != "" {
			t.Logf("gate exclusion %s: %s", rel, reason)
			return
		}
		files = append(files, filepath.Join(root, rel))
	}
	entries, err := os.ReadDir(filepath.Join(root, "configs"))
	require.NoError(t, err)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			add(filepath.Join("configs", e.Name()))
		}
	}
	require.NoError(t, filepath.WalkDir(filepath.Join(root, "deploy", "kustomize"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && filepath.Base(path) == "configs" {
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
					rel, err := filepath.Rel(root, filepath.Join(path, e.Name()))
					require.NoError(t, err)
					add(rel)
				}
			}
		}
		return nil
	}))
	require.NotEmpty(t, files)
	return files
}

func reasonOf(rel string) string {
	if filepath.Base(rel) == "e2e.dev.yaml" {
		return "consumed by cmd/e2e with yaml.KnownFields(true) (test/e2e/config.go) — strict decoding already fails on unknown keys"
	}
	return ""
}

// yamlLeafPaths returns dotted leaf paths; sequences are leaves themselves
// (elements carry no config keys).
func yamlLeafPaths(prefix string, node any) []string {
	m, ok := node.(map[string]any)
	if !ok {
		return []string{prefix}
	}
	var out []string
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if child, isMap := v.(map[string]any); isMap && len(child) > 0 {
			out = append(out, yamlLeafPaths(path, v)...)
			continue
		}
		if child, isMap := v.(map[string]any); isMap && len(child) == 0 {
			out = append(out, path)
			continue
		}
		out = append(out, path)
	}
	return out
}

// structLeafPaths maps every mapstructure leaf path of a struct to the empty
// set (path -> exists).
func structLeafPaths(v reflect.Value) map[string]struct{} {
	out := map[string]struct{}{}
	for p := range structLeafPathsWithFieldNames(v) {
		out[p] = struct{}{}
	}
	return out
}

// structLeafPathsWithFieldNames maps every mapstructure leaf path to its final
// Go field name (used by the reader-coverage direction).
func structLeafPathsWithFieldNames(v reflect.Value) map[string]string {
	out := map[string]string{}
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("mapstructure"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = strings.ToLower(f.Name)
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				walk(ft, path)
				continue
			}
			out[path] = f.Name
		}
	}
	walk(v.Type(), "")
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// productionSelectorNames collects every `.Field` selector name used in
// non-test Go files of cmd/ and internal/ (generated trees excluded).
func productionSelectorNames(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	names := map[string]struct{}{}
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "gen" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				// A production file that does not parse would make the reader
				// scan silently incomplete, so the gate fails loudly instead.
				return fmt.Errorf("parse %s: %w", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					names[sel.Sel.Name] = struct{}{}
				}
				return true
			})
			return nil
		}))
	}
	require.NotEmpty(t, names)
	return names
}
