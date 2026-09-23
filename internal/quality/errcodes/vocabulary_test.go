package errcodes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- canonical emergency reason codes (V12 / D1) ---------------------------
//
// LOCKED_PATH is uppercase, so the lowercase tableCode/quotedSnake rules cannot
// see it at all. Without the vocabulary rule an AC asserting it was silently
// unchecked: the blind spot measured in
// Notes/Audit-2026-09-18/09-req-overturn-triage.md §9.

const reqCanonical = "## 错误模型\n\n" +
	"契约枚举：`EMERGENCY_REASON_CODE_LOCKED_PATH` = 10。\n\n" +
	"| 错误码 | Connect Code |\n| --- | --- |\n" +
	"| `locked_path_short` | `LOCKED_PATH` |\n\n" +
	"## 验收标准\n\n" +
	"- [ ] **AC-900-01** Given x, Then 返回 `LOCKED_PATH`。\n"

const reqCanonicalLongFormAC = "## 错误模型\n\n" +
	"| 错误码 | Connect Code |\n| --- | --- |\n" +
	"| `LOCKED_PATH` | `failed_precondition` |\n\n" +
	"## 验收标准\n\n" +
	"- [ ] **AC-900-01** Given x, Then 返回 `EMERGENCY_REASON_CODE_LOCKED_PATH`。\n"

func canonicalOptions(repoRoot string) Options {
	return Options{RepoRoot: repoRoot, Vocabulary: map[string]struct{}{"LOCKED_PATH": {}}}
}

func TestCheck_CountsCanonicalReasonCodeAssertedByAnAC(t *testing.T) {
	repoRoot, reqPath := write(t, map[string]string{
		"repo/internal/emergency.go": `package x

func f() error {
	return emergencyDetailError(1, orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_LOCKED_PATH, "m", false)
}
`,
		"REQ-900-sample.md": reqCanonical,
	})

	result, err := Check(reqPath, canonicalOptions(repoRoot))
	require.NoError(t, err)

	// The lowercase rule still sees nothing here (no lowercase code is both in
	// the table and in an AC); the vocabulary rule contributes LOCKED_PATH.
	assert.Equal(t, 1, result.CodesChecked)
	assert.Empty(t, result.Violations)
}

func TestCheck_ReportsCanonicalReasonCodeWithNoEmitter(t *testing.T) {
	repoRoot, reqPath := write(t, map[string]string{
		"repo/internal/x.go": "package x\n",
		"REQ-900-sample.md":  reqCanonical,
	})

	result, err := Check(reqPath, canonicalOptions(repoRoot))
	require.NoError(t, err)

	require.Len(t, result.Violations, 1)
	assert.Equal(t, "LOCKED_PATH", result.Violations[0].Code)
}

// Precision test: naming the enum member is NOT emission. A code that only
// appears in a test assertion, or only as a bare reference, must still be
// reported. This is the test that fails if the emitter check is ever weakened
// to an identifier grep.
func TestCheck_EnumReferenceAloneIsNotEmission(t *testing.T) {
	repoRoot, reqPath := write(t, map[string]string{
		// Referenced in a test assertion: the shape emergency_test.go uses.
		"repo/internal/emergency_test.go": `package x

func TestT(t *testing.T) {
	_ = orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_LOCKED_PATH
}
`,
		// Referenced in production code, but never passed to a constructor.
		"repo/internal/emergency.go": `package x

var reasonName = "EMERGENCY_REASON_CODE_LOCKED_PATH"
`,
		"REQ-900-sample.md": reqCanonical,
	})

	result, err := Check(reqPath, canonicalOptions(repoRoot))
	require.NoError(t, err)

	require.Len(t, result.Violations, 1)
	assert.Equal(t, "LOCKED_PATH", result.Violations[0].Code)
}

// The AC may name the long form while the error model names the short form; the
// two sides are matched independently.
func TestCheck_MatchesCanonicalReasonCodeAcrossForms(t *testing.T) {
	repoRoot, reqPath := write(t, map[string]string{
		"repo/internal/emergency.go": `package x

func f() error {
	return emergencyError(1, orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_LOCKED_PATH)
}
`,
		"REQ-900-sample.md": reqCanonicalLongFormAC,
	})

	result, err := Check(reqPath, canonicalOptions(repoRoot))
	require.NoError(t, err)
	assert.Equal(t, 1, result.CodesChecked)
	assert.Empty(t, result.Violations)
}

// A code outside the vocabulary must not be picked up, however it is written:
// the vocabulary is what keeps the uppercase rule from re-introducing the field
// name noise measured in §9.2 (V-c).
func TestCheck_IgnoresUppercaseCodesOutsideTheVocabulary(t *testing.T) {
	repoRoot, reqPath := write(t, map[string]string{
		"repo/internal/x.go": "package x\n",
		"REQ-900-sample.md": "## 错误模型\n\n| 错误码 |\n| --- |\n| `E2E_RUN_ID` |\n\n" +
			"## 验收标准\n\n- [ ] **AC-900-01** Given x, Then `E2E_RUN_ID`。\n",
	})

	result, err := Check(reqPath, canonicalOptions(repoRoot))
	require.NoError(t, err)
	assert.Equal(t, 0, result.CodesChecked)
	assert.Empty(t, result.Violations)
}

func TestCanonicalVocabulary_UnionsAcrossDocuments(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "REQ-a.md")
	b := filepath.Join(dir, "REQ-b.md")
	require.NoError(t, os.WriteFile(a, []byte("`EMERGENCY_REASON_CODE_LOCKED_PATH`\n"), 0o644))
	require.NoError(t, os.WriteFile(b, []byte("`EMERGENCY_REASON_CODE_KILL_SWITCH_DISABLED`\n"), 0o644))

	vocabulary, err := CanonicalVocabulary([]string{a, b})
	require.NoError(t, err)
	assert.Len(t, vocabulary, 2)
	assert.Contains(t, vocabulary, "LOCKED_PATH")
	assert.Contains(t, vocabulary, "KILL_SWITCH_DISABLED")
}
