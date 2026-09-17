package httpcollections

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectionsPointAtMountedProcedures is the live gate: every request in
// api/kulala must name a real RPC, every placeholder must resolve, and the env
// ports must come from configs/*.dev.yaml (dev) and the kustomize NodePorts
// (cluster).
func TestCollectionsPointAtMountedProcedures(t *testing.T) {
	findings, err := Check(repoRoot(t))
	require.NoError(t, err)
	assert.Emptyf(t, findings, "api/kulala drifted from the Connect contract:\n%s", join(findings))
}

// TestCollectionGateRejectsHistoricalShape is the negative control: the shapes
// the gate exists to stop (REST path, unknown procedure, hard-coded URL,
// unresolved variable, dead port) must all be reported.
func TestCollectionGateRejectsHistoricalShape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "api/proto/auth/v1/auth.proto", `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login(LoginRequest) returns (LoginResponse);
}
`)
	writeFile(t, root, "http-client.env.json", `{
  "dev": {"AUTH_URL": "http://localhost:8085", "CUSTOMER_ID": "c"},
  "cluster": {"AUTH_URL": "http://localhost:30085", "CUSTOMER_ID": "c"}
}`)
	writeFile(t, root, "configs/auth.dev.yaml", "http_port: 8085\n")
	writeFile(t, root, "deploy/kustomize/services/auth.yaml", "nodePort: 30085\n")
	writeFile(t, root, "api/kulala/auth.http", `### valid
# @name Login
POST {{AUTH_URL}}/auth.v1.AuthService/Login
Content-Type: application/json

{"customerId": "{{CUSTOMER_ID}}"}

### REST leftover on a dead port
GET http://localhost:8081/api/v1/init

### unknown procedure
POST {{AUTH_URL}}/auth.v1.AuthService/Nope
Content-Type: application/json

{}

### unresolved placeholder
POST {{AUTH_URL}}/auth.v1.AuthService/Login
Content-Type: application/json

{"customerId": "{{NOT_DEFINED_ANYWHERE}}"}
`)

	findings, err := Check(root)
	require.NoError(t, err)
	details := join(findings)
	assert.Contains(t, details, "/api/v1/init", "a REST-shaped request must be reported")
	assert.Contains(t, details, "auth.v1.AuthService/Nope", "an unknown procedure must be reported")
	assert.Contains(t, details, "NOT_DEFINED_ANYWHERE", "an unresolved placeholder must be reported")
}

// TestCollectionGateRejectsDeadPorts covers the env side on its own: a port that
// no config or service declares is exactly how the collections rotted.
func TestCollectionGateRejectsDeadPorts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "api/proto/auth/v1/auth.proto", `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login(LoginRequest) returns (LoginResponse);
}
`)
	writeFile(t, root, "configs/auth.dev.yaml", "http_port: 8085\n")
	writeFile(t, root, "deploy/kustomize/services/auth.yaml", "nodePort: 30085\n")
	writeFile(t, root, "http-client.env.json", `{
  "dev": {"AUTH_URL": "http://localhost:8081", "CUSTOMER_ID": "c"},
  "cluster": {"AUTH_URL": "http://localhost:30085", "CUSTOMER_ID": "c"}
}`)
	writeFile(t, root, "api/kulala/auth.http", `### Login
# @name Login
POST {{AUTH_URL}}/auth.v1.AuthService/Login
Content-Type: application/json

{"customerId": "{{CUSTOMER_ID}}"}
`)

	findings, err := Check(root)
	require.NoError(t, err)
	details := join(findings)
	assert.Contains(t, details, "no configs/*.dev.yaml http_port declares", "a dead dev port must be reported")
}

// TestUnreachableSurfaceIsAllowed documents the operator.http case: a collection
// may ship no requests when it says so, but not silently.
func TestUnreachableSurfaceIsAllowed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "api/proto/auth/v1/auth.proto", `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login(LoginRequest) returns (LoginResponse);
}
`)
	writeFile(t, root, "configs/auth.dev.yaml", "http_port: 8085\n")
	writeFile(t, root, "deploy/kustomize/services/auth.yaml", "nodePort: 30085\n")
	writeFile(t, root, "http-client.env.json", `{
  "dev": {"AUTH_URL": "http://localhost:8085"},
  "cluster": {"AUTH_URL": "http://localhost:30085"}
}`)
	writeFile(t, root, "api/kulala/operator.http", "### NOT REACHABLE: requires a client certificate\n")
	writeFile(t, root, "api/kulala/silent.http", "### nothing here\n")

	findings, err := Check(root)
	require.NoError(t, err)
	details := join(findings)
	assert.NotContains(t, details, "operator.http")
	assert.Contains(t, details, "silent.http", "an empty collection must say why it is empty")
}

func join(findings []Finding) string {
	out := ""
	for _, finding := range findings {
		out += finding.String() + "\n"
	}
	return out
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir, "go.mod not found above %s", dir)
		dir = parent
	}
}
