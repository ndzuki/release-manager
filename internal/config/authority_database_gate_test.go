package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// authorityDatabase is the database a deployment's authorization projection is
// compiled from (TASK-104).
type authorityDatabase struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type serviceConfigFile struct {
	Database authorityDatabase `yaml:"database"`
}

func readAuthorityDatabase(t *testing.T, root, rel string) authorityDatabase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoErrorf(t, err, "read %s", rel)
	var parsed serviceConfigFile
	require.NoErrorf(t, yaml.Unmarshal(raw, &parsed), "parse %s", rel)
	return parsed.Database
}

func sameDatabase(a, b authorityDatabase) bool {
	return a.Driver == b.Driver && strings.TrimSpace(a.DSN) == strings.TrimSpace(b.DSN)
}

// TestManagementPlaneSharesOneAuthorityDatabase is the TASK-104 gate: the
// orchestrator compiles its Casbin projection from the organizations and
// org_members rows that release-auth owns (internal/auth/casbin.go
// compileAuthorizationRules), so the two services must open the same database in
// every profile. Splitting them — which the host-run profile used to do with
// data/auth.db + data/orchestrator.db — leaves the orchestrator with an empty
// policy, and then every Casbin procedure including ListCustomers/CreateCustomer
// answers permission_denied, so a fresh environment cannot bootstrap at all.
func TestManagementPlaneSharesOneAuthorityDatabase(t *testing.T) {
	root := repoRoot(t)
	profiles := []struct {
		name       string
		auth       string
		orchestr   string
		notifierDB string
	}{
		{
			name:       "host-run configs/*.dev.yaml",
			auth:       "configs/auth.dev.yaml",
			orchestr:   "configs/orchestrator.dev.yaml",
			notifierDB: "configs/notifier.dev.yaml",
		},
		{
			name:       "k3d deploy/kustomize/dev overlay",
			auth:       "deploy/kustomize/dev/configs/auth.dev.yaml",
			orchestr:   "deploy/kustomize/dev/configs/orchestrator.dev.yaml",
			notifierDB: "deploy/kustomize/dev/configs/notifier.dev.yaml",
		},
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			auth := readAuthorityDatabase(t, root, profile.auth)
			orchestrator := readAuthorityDatabase(t, root, profile.orchestr)
			require.NotEmptyf(t, auth.DSN, "%s: database.dsn is required", profile.auth)
			assert.Truef(t, sameDatabase(auth, orchestrator),
				"release-auth (%s) and release-orchestrator (%s) must share one authority database, got %q vs %q",
				profile.auth, profile.orchestr, auth.DSN, orchestrator.DSN)

			// The notifier is the documented exception: it owns a separate
			// authority (its own migrations FS), so it must NOT be dragged into
			// the shared database by a careless edit.
			notifier := readAuthorityDatabase(t, root, profile.notifierDB)
			require.NotEmptyf(t, notifier.DSN, "%s: database.dsn is required", profile.notifierDB)
			assert.Falsef(t, sameDatabase(auth, notifier),
				"release-notifier must keep its own database (%s = %q)", profile.notifierDB, notifier.DSN)
		})
	}
}

// TestAuthorityDatabaseGateRejectsASplit is the negative control: the helper the
// gate is built on must report two different databases as different.
func TestAuthorityDatabaseGateRejectsASplit(t *testing.T) {
	same := authorityDatabase{Driver: "sqlite", DSN: "data/management.db"}
	assert.True(t, sameDatabase(same, authorityDatabase{Driver: "sqlite", DSN: " data/management.db "}),
		"surrounding whitespace must not defeat the comparison")
	assert.False(t, sameDatabase(same, authorityDatabase{Driver: "sqlite", DSN: "data/orchestrator.db"}),
		"two SQLite files are two authorities")
	assert.False(t, sameDatabase(same, authorityDatabase{Driver: "postgres", DSN: "data/management.db"}),
		"the driver is part of the identity")
}
