package errcodes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// write lays out a throwaway repo plus one requirement document.
func write(t *testing.T, files map[string]string) (repoRoot, reqPath string) {
	t.Helper()
	dir := t.TempDir()
	repoRoot = filepath.Join(dir, "repo")
	require.NoError(t, os.MkdirAll(repoRoot, 0o755))
	for name, body := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	return repoRoot, filepath.Join(dir, "REQ-900-sample.md")
}

const reqBoth = "## 错误模型\n\n| 错误码 | Connect Code |\n| --- | --- |\n| `alpha_code` | `failed_precondition` |\n| `table_only_code` | `failed_precondition` |\n\n## 验收标准\n\n- [ ] **AC-900-01** Given x, When y, Then `alpha_code`。\n- [ ] **AC-900-02** Given x, When y, Then 返回 `field_name_only`。\n"

func TestCheck_AssertsOnlyCodesInBothTableAndAC(t *testing.T) {
	repoRoot, reqPath := write(t, map[string]string{
		"repo/internal/x.go": `package x

const alpha = "alpha_code"
`,
		"REQ-900-sample.md": reqBoth,
	})

	result, err := Check(reqPath, Options{RepoRoot: repoRoot})
	require.NoError(t, err)

	// table_only_code is in the error model but no AC asserts it, and
	// field_name_only is an AC token that is not an error-model code: both are
	// outside the rule's scope, so only alpha_code is checked.
	assert.Equal(t, 1, result.CodesChecked)
	assert.Empty(t, result.Violations)
}

func TestCheck_ReportsAnAssertedCodeWithNoEmitter(t *testing.T) {
	repoRoot, reqPath := write(t, map[string]string{
		"repo/internal/x.go": "package x\n",
		"REQ-900-sample.md":  reqBoth,
	})

	result, err := Check(reqPath, Options{RepoRoot: repoRoot})
	require.NoError(t, err)

	require.Len(t, result.Violations, 1)
	assert.Equal(t, "alpha_code", result.Violations[0].Code)
	assert.Equal(t, "REQ-900-sample", result.Violations[0].REQ)
}

func TestCheck_AcceptsAShellEmitter(t *testing.T) {
	repoRoot, reqPath := write(t, map[string]string{
		// The dev lifecycle scripts carry their own stable codes (REQ-065).
		"repo/deploy/dev/dev.sh": "die() { echo \"$1\"; }\ndie \"alpha_code\"\n",
		"REQ-900-sample.md":      reqBoth,
	})

	result, err := Check(reqPath, Options{RepoRoot: repoRoot})
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
}

func TestCheck_HonoursExceptions(t *testing.T) {
	repoRoot, reqPath := write(t, map[string]string{
		"repo/internal/x.go": "package x\n",
		"REQ-900-sample.md":  reqBoth,
	})

	result, err := Check(reqPath, Options{
		RepoRoot:   repoRoot,
		Exceptions: map[string]string{"alpha_code": "tracked by TASK-999"},
	})
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
}

// A code the error model names but no AC asserts is an error-model/AC coverage
// mismatch, not a code defect: the gate must stay quiet about it.
func TestCheck_IgnoresErrorModelCodesWithoutAnAC(t *testing.T) {
	repoRoot, reqPath := write(t, map[string]string{
		"repo/internal/x.go": "package x\n",
		"REQ-900-sample.md":  "## 错误模型\n\n| 错误码 |\n| --- |\n| `never_asserted_code` |\n\n## 验收标准\n\n- [ ] **AC-900-01** Given x, Then y。\n",
	})

	result, err := Check(reqPath, Options{RepoRoot: repoRoot})
	require.NoError(t, err)
	assert.Equal(t, 0, result.CodesChecked)
	assert.Empty(t, result.Violations)
}
