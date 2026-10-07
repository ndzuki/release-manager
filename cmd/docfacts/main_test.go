package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTree lays out the three source files the audit derives facts from plus the
// markdown it scans, so run() can be exercised end to end without the repository.
func writeTree(t *testing.T, docs map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"web/nginx.conf": `    location ^~ /auth.v1. {
        proxy_pass http://auth:8085;
    }
    location ^~ /audit.v1. {
        proxy_pass http://api:8088;
    }
`,
		"web/vite.config.ts": `    proxy: {
      '/audit.v1.': {
        target: 'http://127.0.0.1:8088',
      },
    },
`,
		"deploy/dev/lib/host.sh": "DEV_PORTS=(8082 8083 8088)\n",
	}
	for name, content := range docs {
		files[name] = content
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return root
}

func runAudit(t *testing.T, root string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run([]string{"-root", root}, &out, &errOut)
	return out.String(), errOut.String(), code
}

// The audit reports a contradiction and still exits 0: it is not a gate.
func TestRunReportsContradictionAndExitsZero(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/x.md": "# proxy\n\n- `/audit.v1.` 未被 nginx 代理\n",
	})
	stdout, _, code := runAudit(t, root)
	assert.Equal(t, 0, code, "the audit never fails the build")
	assert.Contains(t, stdout, "absent-prefix")
	assert.Contains(t, stdout, "docs/x.md:3")
	assert.Contains(t, stdout, "web/nginx.conf:4")
	assert.Contains(t, stdout, "not a gate")
}

// A clean tree reports zero findings, which is the state the fix commits left the
// repository in for these classes.
func TestRunCleanTree(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/x.md": "# proxy\n\n- `/audit.v1.` 现在有 nginx location（web/nginx.conf:4）\n",
	})
	stdout, _, code := runAudit(t, root)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "no claim contradicts its source of truth")
}

// The qualifier written above a claim keeps the record out of the report, which is
// what lets docs/ux-review.md keep its pre-fix evidence -- including when the block
// spends a line or two describing the failure before naming the prefix.
func TestRunExemptsHistoricalRecords(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/b5.md": "# B5\n\n- 容器入口（**修复前**）：\n  说明一行\n  又是一行\n  `/audit.v1.` 未被 nginx 代理\n",
	})
	stdout, _, code := runAudit(t, root)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "no claim contradicts its source of truth")
}

func TestRunLimitsOutput(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/x.md": "宿主端口段 8082-8087\n宿主端口段 8082-8087\n宿主端口段 8082-8087\n",
	})
	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", root, "-max", "2"}, &stdout, &stderr)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout.String(), "... 1 more")
	assert.Contains(t, stdout.String(), "3 finding(s)")
}

// A missing source of truth is a usage error, not a silent pass: the facts cannot
// be derived, so the audit says so instead of reporting zero findings.
func TestRunMissingSourceFailsLoudly(t *testing.T) {
	root := t.TempDir()
	stdout, stderr, code := runAudit(t, root)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "nginx.conf")
	assert.NotContains(t, stdout, "no claim contradicts")
}

// The scan skips vendor trees, so a stray copy under node_modules cannot inflate or
// silence the report.
func TestRunSkipsIgnoredTrees(t *testing.T) {
	root := writeTree(t, map[string]string{
		"node_modules/pkg/README.md": "`/audit.v1.` 未被 nginx 代理\n",
		".worktrees/other/docs.md":   "`/audit.v1.` 未被 nginx 代理\n",
	})
	stdout, _, code := runAudit(t, root)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "no claim contradicts its source of truth")
}
