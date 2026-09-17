// Package httpcollections validates the Kulala collections under api/kulala
// against the Connect contract in api/proto (TASK-093).
//
// The collections were written before ADR-002 (single-port Connect contract), so
// they drifted into a REST shape that no service serves. This gate keeps them
// honest without needing a running cluster:
//
//   - every request line must be `METHOD {{ENV_VAR}}/<package>.<Service>/<Method>`
//     with the procedure present in api/proto (or the file must declare itself
//     NOT REACHABLE in a comment, which is how the mTLS-only operator surface is
//     recorded);
//   - every `{{variable}}` must resolve: a document variable in the same file, a
//     key of every http-client.env.json profile, or a documented process/captured
//     variable;
//   - the env file's hosts/ports must come from the same source of truth as the
//     services: configs/*.dev.yaml for the `dev` profile and the kustomize
//     NodePorts for the `cluster` profile;
//   - no collection may quietly go back to the removed REST surface.
package httpcollections

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Finding is one violation of the collection contract.
type Finding struct {
	File   string
	Detail string
}

func (f Finding) String() string { return f.File + ": " + f.Detail }

var (
	requestLinePattern = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|GRPC)\s+(\S+)`)
	connectPathPattern = regexp.MustCompile(`^\{\{([A-Z0-9_]+)\}\}/([A-Za-z0-9_.]+)/([A-Za-z0-9_]+)$`)
	variablePattern    = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)
	documentVarPattern = regexp.MustCompile(`^@([A-Za-z0-9_]+)\s*=`)
	restPathPattern    = regexp.MustCompile(`/api/v\d`)
	packagePattern     = regexp.MustCompile(`^package\s+([A-Za-z0-9_.]+)\s*;`)
	servicePattern     = regexp.MustCompile(`^service\s+([A-Za-z0-9_]+)\s*\{`)
	rpcPattern         = regexp.MustCompile(`^\s*rpc\s+([A-Za-z0-9_]+)\s*\(`)
	httpPortPattern    = regexp.MustCompile(`(?m)^\s*http_port:\s*(\d+)`)
	nodePortPattern    = regexp.MustCompile(`(?m)^\s*nodePort:\s*(\d+)`)
	urlPattern         = regexp.MustCompile(`^http://(localhost|127\.0\.0\.1):(\d+)$`)
)

// processVariables are resolved outside the file and the env profile: a token
// captured by a post-request script, or a secret the caller exports before
// running the collection (documented in docs/http-collections.md). The values
// are the placeholder NAMES and where they come from, never a credential.
//
//nolint:gosec // G101: variable names paired with their source, not secrets
var processVariables = map[string]string{
	"AUTH_TOKEN":         "captured by Login's post-request script",
	"DEV_CI_API_KEY":     "exported from data/dev-service-tokens/ci-api-key",
	"DEV_HARBOR_API_KEY": "exported from data/dev-service-tokens/harbor-service-token",
	"DEV_ADMIN_PASSWORD": "exported by dev.sh into data/dev-credentials.env",
}

// EnvProfiles are the required profiles of http-client.env.json.
var EnvProfiles = []string{"dev", "cluster"}

// nonConnectRoutes are documented HTTP routes that are deliberately not Connect
// procedures. Keeping them here (instead of skipping unknown paths) means a new
// non-Connect route still has to be registered deliberately.
var nonConnectRoutes = map[string]string{
	"webhooks/harbor": "Harbor artifact ingress (TASK-102): a plain CloudEvents HTTP route with its own key, forwarded to BundleService.RecordArtifactEvent",
}

// clusterURLExceptions are the env variables that intentionally do not resolve to
// a kustomize NodePort.
var clusterURLExceptions = map[string]string{
	"API_URL": "release-api is host-run only in dev (docs/api.md §1 note 2), so the cluster profile reuses the local port",
}

// unreachableMarker lets a collection state that its surface cannot be driven
// from Kulala (a client certificate is required) instead of shipping a request
// that cannot work.
const unreachableMarker = "NOT REACHABLE"

// Check validates one repository rooted at root and returns every finding.
func Check(root string) ([]Finding, error) {
	procedures, err := loadProcedures(root)
	if err != nil {
		return nil, err
	}
	env, err := loadEnv(root)
	if err != nil {
		return nil, err
	}
	devPorts, err := configPorts(filepath.Join(root, "configs"), httpPortPattern)
	if err != nil {
		return nil, err
	}
	clusterPorts, err := configPorts(filepath.Join(root, "deploy", "kustomize", "services"), nodePortPattern)
	if err != nil {
		return nil, err
	}

	var findings []Finding
	collections, err := filepath.Glob(filepath.Join(root, "api", "kulala", "*.http"))
	if err != nil {
		return nil, err
	}
	sort.Strings(collections)
	if len(collections) == 0 {
		return nil, fmt.Errorf("no collections found under api/kulala")
	}

	for _, path := range collections {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil, relErr
		}
		rel = filepath.ToSlash(rel)
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		fileFindings := checkCollection(rel, string(content), procedures, env)
		findings = append(findings, fileFindings...)
	}

	findings = append(findings, checkEnv(env, devPorts, clusterPorts)...)
	return findings, nil
}

// stripEnvPrefix removes a leading {{ENV}}/ so a non-Connect route can be
// recognised on its own path.
func stripEnvPrefix(target string) string {
	if idx := strings.Index(target, "}}/"); idx >= 0 {
		return target[idx+3:]
	}
	return target
}

func checkCollection(rel, content string, procedures map[string]bool, env map[string]map[string]string) []Finding {
	var findings []Finding
	lines := strings.Split(content, "\n")

	documentVars := map[string]bool{}
	for _, line := range lines {
		if match := documentVarPattern.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			documentVars[match[1]] = true
		}
	}

	requests := 0
	for number, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if restPathPattern.MatchString(trimmed) {
			findings = append(findings, Finding{rel, fmt.Sprintf("line %d: REST path on the removed /api/vN surface", number+1)})
		}
		match := requestLinePattern.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		requests++
		findings = append(findings, checkRequestTarget(rel, number+1, match[2], procedures)...)
	}

	if requests == 0 && !strings.Contains(content, unreachableMarker) {
		findings = append(findings, Finding{rel, "has no runnable request and does not declare " + unreachableMarker})
	}

	return append(findings, checkPlaceholders(rel, content, documentVars, env)...)
}

// checkRequestTarget validates one request line's target.
func checkRequestTarget(rel string, line int, target string, procedures map[string]bool) []Finding {
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return []Finding{{rel, fmt.Sprintf("line %d: hard-coded URL %q; use an env variable", line, target)}}
	}
	if route := stripEnvPrefix(target); nonConnectRoutes[route] != "" {
		return nil
	}
	pathMatch := connectPathPattern.FindStringSubmatch(target)
	if pathMatch == nil {
		return []Finding{{rel, fmt.Sprintf("line %d: %q is not METHOD {{ENV}}/<package>.<Service>/<Method>", line, target)}}
	}
	if procedure := pathMatch[2] + "/" + pathMatch[3]; !procedures[procedure] {
		return []Finding{{rel, fmt.Sprintf("line %d: %s is not an RPC in api/proto", line, procedure)}}
	}
	return nil
}

// checkPlaceholders reports every {{variable}} that cannot be resolved.
func checkPlaceholders(rel, content string, documentVars map[string]bool, env map[string]map[string]string) []Finding {
	var findings []Finding
	seen := map[string]bool{}
	for _, match := range variablePattern.FindAllStringSubmatch(content, -1) {
		name := match[1]
		if seen[name] || documentVars[name] || envDefinesEveryProfile(env, name) {
			continue
		}
		if _, ok := processVariables[name]; ok {
			continue
		}
		seen[name] = true
		findings = append(findings, Finding{rel, fmt.Sprintf("{{%s}} is not defined by the file, any env profile, or the documented process variables", name)})
	}
	return findings
}

func checkEnv(env map[string]map[string]string, devPorts, clusterPorts map[string]bool) []Finding {
	var findings []Finding
	for _, profile := range EnvProfiles {
		if _, ok := env[profile]; !ok {
			findings = append(findings, Finding{"http-client.env.json", fmt.Sprintf("profile %q is missing", profile)})
		}
	}
	for profile, vars := range env {
		for name, value := range vars {
			if !strings.HasSuffix(name, "_URL") {
				continue
			}
			match := urlPattern.FindStringSubmatch(value)
			if match == nil {
				findings = append(findings, Finding{"http-client.env.json", fmt.Sprintf("%s.%s = %q is not http://localhost:<port>", profile, name, value)})
				continue
			}
			port, convErr := strconv.Atoi(match[2])
			if convErr != nil {
				findings = append(findings, Finding{"http-client.env.json", fmt.Sprintf("%s.%s has an unparsable port", profile, name)})
				continue
			}
			switch profile {
			case "dev":
				if !devPorts[strconv.Itoa(port)] {
					findings = append(findings, Finding{"http-client.env.json", fmt.Sprintf("dev.%s uses port %d, which no configs/*.dev.yaml http_port declares", name, port)})
				}
			case "cluster":
				if _, ok := clusterURLExceptions[name]; ok {
					continue
				}
				if !clusterPorts[strconv.Itoa(port)] {
					findings = append(findings, Finding{"http-client.env.json", fmt.Sprintf("cluster.%s uses port %d, which no kustomize service nodePort declares", name, port)})
				}
			}
		}
	}
	return findings
}

func loadProcedures(root string) (map[string]bool, error) {
	procedures := map[string]bool{}
	protoRoot := filepath.Join(root, "api", "proto")
	err := fs.WalkDir(os.DirFS(protoRoot), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		content, readErr := fs.ReadFile(os.DirFS(protoRoot), path)
		if readErr != nil {
			return readErr
		}
		pkg := ""
		service := ""
		for _, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if match := packagePattern.FindStringSubmatch(trimmed); match != nil {
				pkg = match[1]
				continue
			}
			if match := servicePattern.FindStringSubmatch(trimmed); match != nil {
				service = match[1]
				continue
			}
			if trimmed == "}" {
				service = ""
				continue
			}
			if service == "" {
				continue
			}
			if match := rpcPattern.FindStringSubmatch(line); match != nil && pkg != "" {
				procedures[pkg+"."+service+"/"+match[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(procedures) == 0 {
		return nil, fmt.Errorf("no RPCs parsed from %s", protoRoot)
	}
	return procedures, nil
}

func loadEnv(root string) (map[string]map[string]string, error) {
	content, err := os.ReadFile(filepath.Join(root, "http-client.env.json"))
	if err != nil {
		return nil, err
	}
	var env map[string]map[string]string
	if err := json.Unmarshal(content, &env); err != nil {
		return nil, fmt.Errorf("parse http-client.env.json: %w", err)
	}
	return env, nil
}

func envDefinesEveryProfile(env map[string]map[string]string, name string) bool {
	if len(env) == 0 {
		return false
	}
	for _, profile := range EnvProfiles {
		vars, ok := env[profile]
		if !ok {
			return false
		}
		if _, ok := vars[name]; !ok {
			return false
		}
	}
	return true
}

func configPorts(dir string, pattern *regexp.Regexp) (map[string]bool, error) {
	ports := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return ports, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		for _, match := range pattern.FindAllStringSubmatch(string(content), -1) {
			ports[match[1]] = true
		}
	}
	return ports, nil
}
