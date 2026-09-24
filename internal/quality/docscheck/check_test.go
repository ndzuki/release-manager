package docscheck

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	return root
}

func TestAudit_ReportsASymbolOutsideTheWindow(t *testing.T) {
	root := write(t, map[string]string{
		"internal/x/x.go": "package x\n\n// filler\n\nfunc CreateTrustRoot() {}\n",
		"docs/a.md":       "- 入口是 `CreateTrustRoot`（`internal/x/x.go:5`）。\n",
	})

	result, err := Audit(root)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Checked)
	assert.Empty(t, result.Findings, "the symbol sits on the cited line")
}

func TestAudit_FlagsASymbolTheProseNamesButTheRangeMisses(t *testing.T) {
	root := write(t, map[string]string{
		"internal/x/x.go": "package x\n\nfunc CreateTrustRoot() {}\n\n\n\n\n\n\n\nfunc HandleThing() {}\n",
		"docs/a.md":       "- 入口是 `HandleThing`（`internal/x/x.go:3`）。\n",
	})

	result, err := Audit(root)
	require.NoError(t, err)
	require.Len(t, result.Findings, 1)
	assert.Equal(t, "HandleThing", result.Findings[0].Symbol)
	assert.Contains(t, result.Findings[0].Detail, "line 11")
}

// A line that already carries the exemption is skipped, which is how a real
// false positive is recorded without weakening the rule.
func TestAudit_HonoursTheCheckDocsIgnoreMarker(t *testing.T) {
	root := write(t, map[string]string{
		"internal/x/x.go": "package x\n\nfunc CreateTrustRoot() {}\n",
		"docs/a.md":       "- 无 `HandleThing` 脱敏（`internal/x/x.go:3`）。 <!-- check-docs:ignore 陈述某物不存在 -->\n",
	})

	result, err := Audit(root)
	require.NoError(t, err)
	assert.Empty(t, result.Findings)
}

// The span that belongs to the NEXT citation on the same line must not be used as
// this citation's anchor: that is the shorthand-chain false positive.
func TestAudit_IgnoresTheSpanOfANeighbouringCitation(t *testing.T) {
	root := write(t, map[string]string{
		"internal/x/x.go": "package x\n\nfunc CreateTrustRoot() {}\n\n\n\n\n\n\n\nfunc HandleThing() {}\n",
		"docs/a.md":       "- 入口 `internal/x/x.go:3`、`HandleThing`（`internal/x/x.go:11`）。\n",
	})

	result, err := Audit(root)
	require.NoError(t, err)
	assert.Empty(t, result.Findings, "HandleThing belongs to the second citation, not the first")
}

// Single capitalised words and YAML keys are not symbols: TASK-113 measured them
// as noise.
func TestAudit_IgnoresWeakAnchors(t *testing.T) {
	root := write(t, map[string]string{
		"internal/x/x.go": "package x\n\nfunc CreateTrustRoot() {}\n\n\n\n\n\n\n\nfunc HandleThing() {}\n",
		"docs/a.md":       "- `Run` 的说明（`internal/x/x.go:11`）。\n",
	})

	result, err := Audit(root)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Checked, "a single-hump name is not a strong symbol")
}

// The repository keeps its own git worktrees under .worktrees/. Each is a full
// checkout, so auditing them re-reports every finding once per worktree — 13
// copies once made 93.6% of this audit's output duplicate noise. The copy must
// not be counted as a second file, and must not produce a second finding.
func TestAudit_SkipsTheRepositorysOwnWorktreeCopies(t *testing.T) {
	body := "- 入口是 `CreateTrustRoot`（`internal/x/x.go:1`）。\n"
	root := write(t, map[string]string{
		"internal/x/x.go":           "package x\n",
		"docs/a.md":                 body,
		".worktrees/t093/docs/a.md": body,
	})

	result, err := Audit(root)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Files, "the worktree copy must not be counted as a markdown file")
	require.Len(t, result.Findings, 1, "the real citation is still reported exactly once")
	assert.Contains(t, result.Findings[0].String(), "docs/a.md",
		"the surviving finding must be the real document, not the worktree copy")
}

// `data` and `e2e-results` are gitignored build/run output, like `bin`. A
// citation inside them is not a claim about the repository, so they must not be
// audited (they contributed findings that no one could act on).
func TestAudit_SkipsGeneratedOutputDirectories(t *testing.T) {
	body := "- 入口是 `CreateTrustRoot`（`internal/x/x.go:1`）。\n"
	root := write(t, map[string]string{
		"internal/x/x.go":              "package x\n",
		"docs/a.md":                    body,
		"data/evidence/REPORT.md":      body,
		"e2e-results/AC-066-report.md": body,
	})

	result, err := Audit(root)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Files, "only docs/a.md is documentation")
	require.Len(t, result.Findings, 1, "the real citation is still reported exactly once")
	assert.Contains(t, result.Findings[0].String(), "docs/a.md",
		"the surviving finding must be the real document, not a generated report")
}
