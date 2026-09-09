package e2e_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/release-manager/test/e2e"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigResolvesPasswordWithoutSerializingSecret(t *testing.T) {
	const password = "black-box-secret"
	t.Setenv("E2E_RUNNER_PASSWORD", password)
	configPath := filepath.Join(t.TempDir(), "env-config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(validConfigYAML), 0o600))

	config, err := e2e.LoadConfig(configPath)
	require.NoError(t, err)
	assert.Equal(t, password, config.Password())

	jsonData, err := json.Marshal(config)
	require.NoError(t, err)
	assert.NotContains(t, string(jsonData), password)
	assert.NotContains(t, string(jsonData), `"password":`)
	assert.Contains(t, string(jsonData), "password_env")
}

func TestLoadConfigRejectsInvalidInputs(t *testing.T) {

	tests := []struct {
		name       string
		configYAML string
		unsetEnv   bool
		wantField  string
	}{
		{
			name:       "missing orchestrator",
			configYAML: strings.Replace(validConfigYAML, "release_orchestrator: \"http://localhost:8083\"", "release_orchestrator: \"\"", 1),
			wantField:  "endpoints.release_orchestrator",
		},
		{name: "password missing", configYAML: validConfigYAML, unsetEnv: true, wantField: "credentials.e2e_runner.password_env"},
		{
			name: "restart namespace mismatch",
			configYAML: strings.Replace(
				validConfigYAML,
				"namespace: release-manager-dev",
				"namespace: other",
				1,
			),
			wantField: "k3d.restart_targets.namespace",
		},
		{
			name: "restart deployment count",
			configYAML: strings.Replace(
				validConfigYAML,
				"deployments: [auth, operator-gateway, orchestrator]",
				"deployments: [auth]",
				1,
			),
			wantField: "k3d.restart_targets.deployments",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.unsetEnv {
				t.Setenv("E2E_RUNNER_PASSWORD", "")
			} else {
				t.Setenv("E2E_RUNNER_PASSWORD", "pw")
			}
			config, err := e2e.ParseConfig([]byte(test.configYAML))
			assert.Nil(t, config)
			require.Error(t, err)
			assert.ErrorIs(t, err, e2e.ErrConfigInvalid)
			assert.Contains(t, err.Error(), test.wantField)
		})
	}
}

func TestRunIDValidation(t *testing.T) {
	t.Parallel()

	for _, runID := range []string{"ci-20260909", "a", "a-1", strings.Repeat("a", 63)} {
		assert.NoError(t, e2e.ValidateRunID(runID))
	}
	for _, runID := range []string{"", "A", "-bad", "bad-", strings.Repeat("a", 64)} {
		assert.ErrorIs(t, e2e.ValidateRunID(runID), e2e.ErrE2ERunIDInvalid)
	}
}

func TestSnapshotDigestSortsCollectionsAndCloneIsDeep(t *testing.T) {
	t.Parallel()

	snapshot := e2e.FixtureSnapshot{
		CollectedAt: time.Unix(10, 0).UTC(),
		Identity: e2e.SnapshotIdentity{
			Customers:  []e2e.IdentityRef{{ID: "b"}, {ID: "a"}},
			Operations: []e2e.OperationSummary{{ID: "op-2", StateVersion: 2}, {ID: "op-1", StateVersion: 1}},
		},
		Full: json.RawMessage(`{"b":2,"a":1}`),
	}
	clone := snapshot.Clone()
	require.NotNil(t, clone)
	clone.Identity.Customers[0].ID = "changed"
	clone.Full[0] = 'x'
	assert.Equal(t, "b", snapshot.Identity.Customers[0].ID)
	assert.Equal(t, byte('{'), snapshot.Full[0])

	shuffled := snapshot
	shuffled.Identity.Customers = []e2e.IdentityRef{{ID: "a"}, {ID: "b"}}
	shuffled.Identity.Operations = []e2e.OperationSummary{{ID: "op-1", StateVersion: 1}, {ID: "op-2", StateVersion: 2}}
	digestA, err := e2e.StableSnapshotDigest(snapshot)
	require.NoError(t, err)
	digestB, err := e2e.StableSnapshotDigest(shuffled)
	require.NoError(t, err)
	assert.Equal(t, digestA, digestB)
}

func TestAssertSnapshotReturnsSanitizedMismatch(t *testing.T) {
	t.Parallel()

	expected := e2e.FixtureSnapshot{Identity: e2e.SnapshotIdentity{Customers: []e2e.IdentityRef{{ID: "customer-a", Name: "expected"}}}}
	actual := e2e.FixtureSnapshot{Identity: e2e.SnapshotIdentity{Customers: []e2e.IdentityRef{{ID: "customer-a", Name: "password=secret"}}}}
	err := e2e.AssertSnapshot(expected, actual)
	require.Error(t, err)
	var mismatch *e2e.SnapshotMismatchError
	require.True(t, errors.As(err, &mismatch))
	assert.Contains(t, err.Error(), "identity.customers")
	assert.NotContains(t, err.Error(), "password=secret")
	assert.NotContains(t, err.Error(), "secret")
}

const validConfigYAML = `
environment: ci
environment_id: ci-run
endpoints:
  release_orchestrator: "http://localhost:8083"
  release_webhook: "http://localhost:8082"
  release_operator: "https://localhost:8084"
  release_auth: "http://localhost:8085"
  release_notifier: "http://localhost:8086"
  release_api: "http://localhost:8087"
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
  customers: [dev-customer-a, dev-customer-b]
  clusters_per_customer: 2
  fixture_version: fixture-v1
  expected_identity:
    customers: 2
    clusters: 4
    routes_basic: 8
    definitions_basic: 4
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
