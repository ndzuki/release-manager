package taskcheck

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeREQ(t *testing.T, name, frontmatter, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	content := "---\n" + frontmatter + "---\n\n# " + name + "\n\n" + body
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// A delivered+verified REQ with a delivery record is the consistent state.
func TestCheckRequirementsAcceptsADeliveredVerifiedRecord(t *testing.T) {
	path := writeREQ(t, "REQ-901-ok.md",
		"id: \"901\"\nstatus: delivered\nverified: true\n",
		"## 交付记录\n\n判定：delivered · 2026-09-27\n")

	req, err := ParseRequirement(path)
	require.NoError(t, err)
	assert.Equal(t, "delivered", req.Status)
	assert.True(t, req.Verified)
	assert.True(t, req.HasDeliveryRecord)

	result := CheckRequirements([]Requirement{req})
	assert.Empty(t, result.Findings)
	assert.Equal(t, 1, result.Checked)
}

// Negative control: a REQ that carries a delivery record while still accepted
// and unverified is the REQ-065 half-write the gate exists for. Dropping the
// rules in CheckRequirements makes this test fail.
func TestCheckRequirementsRejectsADeliveryRecordOnAnAcceptedRequirement(t *testing.T) {
	path := writeREQ(t, "REQ-902-half.md",
		"id: \"902\"\nstatus: accepted\n",
		"## 交付记录\n\n判定：delivered · 2026-09-18\n")

	req, err := ParseRequirement(path)
	require.NoError(t, err)
	require.True(t, req.HasDeliveryRecord)

	result := CheckRequirements([]Requirement{req})
	kinds := make([]string, 0, len(result.Findings))
	for _, f := range result.Findings {
		kinds = append(kinds, f.Kind)
		assert.Equal(t, SeverityViolation, f.Severity)
	}
	assert.Contains(t, kinds, KindDeliveryRecordNotDelivered,
		"a delivery record must mean delivered")
	assert.Contains(t, kinds, KindDeliveryRecordNotVerified,
		"a delivery record must mean verified")
}

// The two ledger fields must never disagree in either direction.
func TestCheckRequirementsRejectsDeliveredAndVerifiedDisagreement(t *testing.T) {
	deliveredNoVerified := writeREQ(t, "REQ-903-dnv.md",
		"id: \"903\"\nstatus: delivered\n", "## 目标\n无。\n")
	verifiedNoDelivered := writeREQ(t, "REQ-904-vnd.md",
		"id: \"904\"\nstatus: accepted\nverified: true\n", "## 目标\n无。\n")

	for _, tc := range []struct {
		path string
		want string
	}{
		{deliveredNoVerified, KindDeliveredNotVerified},
		{verifiedNoDelivered, KindVerifiedNotDelivered},
	} {
		req, err := ParseRequirement(tc.path)
		require.NoError(t, err)
		result := CheckRequirements([]Requirement{req})
		require.Len(t, result.Findings, 1, "exactly one disagreement expected for %s", tc.path)
		assert.Equal(t, tc.want, result.Findings[0].Kind)
	}
}

// A REQ with none of the three signals is not part of the delivery ledger and
// must not be counted.
func TestCheckRequirementsSkipsUntrackedRequirements(t *testing.T) {
	path := writeREQ(t, "REQ-905-draft.md",
		"id: \"905\"\nstatus: accepted\n", "## 目标\n无。\n")

	req, err := ParseRequirement(path)
	require.NoError(t, err)
	result := CheckRequirements([]Requirement{req})
	assert.Empty(t, result.Findings)
	assert.Zero(t, result.Checked)
}

// A delivery record that STATES it is partial is evidence-in-progress, not a
// claim of delivery: REQ-065 documents "10/11 AC 有可复现证据" while two
// criteria are still open. The partiality must be stated -- a record that does
// not say so is still a violation for a non-delivered REQ. Removing the
// PartialRecord branch from CheckRequirements makes this test fail.
func TestCheckRequirementsAcceptsAStatedPartialRecord(t *testing.T) {
	partial := writeREQ(t, "REQ-908-partial.md",
		"id: \"908\"\nstatus: accepted\n",
		"## 交付记录（部分，2026-09-19）—— 10/11 AC 有可复现证据，未达 delivered\n\n| AC | 证据 |\n| --- | --- |\n")
	req, err := ParseRequirement(partial)
	require.NoError(t, err)
	require.True(t, req.HasDeliveryRecord)
	require.True(t, req.PartialRecord)
	assert.Empty(t, CheckRequirements([]Requirement{req}).Findings,
		"a stated-partial record must not be treated as a delivery claim")

	silent := writeREQ(t, "REQ-909-silent.md",
		"id: \"909\"\nstatus: accepted\n",
		"## 交付记录\n\n判定：delivered\n")
	silentReq, err := ParseRequirement(silent)
	require.NoError(t, err)
	require.False(t, silentReq.PartialRecord)
	assert.NotEmpty(t, CheckRequirements([]Requirement{silentReq}).Findings,
		"a record that does not state partiality still requires delivered + verified")
}
