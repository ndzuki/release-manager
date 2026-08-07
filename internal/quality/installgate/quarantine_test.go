package installgate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadQuarantinesAt_ValidInfrastructureException(t *testing.T) {
	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	path := writeQuarantineFile(t, `version: "1"
exceptions:
  - scenario: cluster-readiness
    rule_id: cluster_unavailable
    owner: release-team
    reason: runner outage
    issue: https://example.invalid/issues/61
    expires_at: "2026-07-30"
`)

	quarantines, err := loadQuarantinesAt(path, now)
	require.NoError(t, err)

	exception, found := quarantines.Match("cluster-readiness", RuleClusterUnavailable)
	require.True(t, found)
	assert.Equal(t, "release-team", exception.Owner)
	_, found = quarantines.Match("another-scenario", RuleClusterUnavailable)
	assert.False(t, found)
}

func TestLoadQuarantinesAt_RejectsExpiredException(t *testing.T) {
	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	path := writeQuarantineFile(t, `version: "1"
exceptions:
  - scenario: cluster-readiness
    rule_id: cluster_unavailable
    owner: release-team
    reason: runner outage
    issue: https://example.invalid/issues/61
    expires_at: "2026-07-27"
`)

	_, err := loadQuarantinesAt(path, now)
	require.Error(t, err)
	assert.ErrorContains(t, err, RuleExpiredException)
}

func TestLoadQuarantinesAt_RejectsProductRule(t *testing.T) {
	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	path := writeQuarantineFile(t, `version: "1"
exceptions:
  - scenario: atomic-cleanup
    rule_id: atomic_cleanup_failed
    owner: release-team
    reason: flaky cleanup
    issue: https://example.invalid/issues/61
    expires_at: "2026-07-30"
`)

	_, err := loadQuarantinesAt(path, now)
	require.Error(t, err)
	assert.ErrorContains(t, err, "cannot be quarantined")
}

func TestLoadQuarantinesAt_RejectsExceptionBeyondSevenDays(t *testing.T) {
	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	path := writeQuarantineFile(t, `version: "1"
exceptions:
  - scenario: cluster-readiness
    rule_id: cluster_unavailable
    owner: release-team
    reason: runner outage
    issue: https://example.invalid/issues/61
    expires_at: "2026-08-05"
`)

	_, err := loadQuarantinesAt(path, now)
	require.Error(t, err)
	assert.ErrorContains(t, err, "seven days")
}

func TestLoadQuarantinesAt_RejectsMissingIssue(t *testing.T) {
	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	path := writeQuarantineFile(t, `version: "1"
exceptions:
  - scenario: cluster-readiness
    rule_id: cluster_unavailable
    owner: release-team
    reason: runner outage
    expires_at: "2026-07-30"
`)

	_, err := loadQuarantinesAt(path, now)
	require.Error(t, err)
	assert.ErrorContains(t, err, "missing issue field")
}

func writeQuarantineFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "install-sdk.quarantine.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}
