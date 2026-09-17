package store_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditInsertPattern matches a direct write into the audit trail.
var auditInsertPattern = regexp.MustCompile(`INSERT\s+(OR\s+IGNORE\s+)?INTO\s+audit_events`)

// auditWriteAllowlist is the complete set of files allowed to write audit_events
// directly, each with the reason it cannot use the asynchronous emitter.
//
// The list is intentionally tiny: TASK-097 converged the write paths so that the
// emitter is the only asynchronous entry and the transactional writers (ADR-009)
// redact through store.SanitizeAuditEvent.
var auditWriteAllowlist = map[string]string{
	"internal/store/sqlite/audit.go":                 "audit event store implementation (emitter persistence)",
	"internal/store/postgres/audit.go":               "audit event store implementation (emitter persistence)",
	"internal/store/sqlite/audit_exports.go":         "export job row plus its audit event are created in one transaction",
	"internal/store/postgres/audit_exports.go":       "export job row plus its audit event are created in one transaction",
	"internal/store/sqlite/operator_management.go":   "operator revocation audit row is transactional with the revocation (ADR-009); redacted via store.SanitizeAuditEvent",
	"internal/store/postgres/operator_management.go": "operator revocation audit row is transactional with the revocation (ADR-009); redacted via store.SanitizeAuditEvent",
}

// sanitizeRequiredFiles are the allowlisted writers that must prove they redact.
var sanitizeRequiredFiles = []string{
	"internal/store/sqlite/operator_management.go",
	"internal/store/postgres/operator_management.go",
}

// scanAuditWrites returns the files that write audit_events directly, split into
// registered and unregistered ones. It walks an fs.FS so the gate itself can be
// tested against a synthetic tree.
func scanAuditWrites(fsys fs.FS) (offenders []string, seen map[string]bool, err error) {
	seen = map[string]bool{}
	err = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", "node_modules", "api", "web", "bin", "data":
				return fs.SkipDir
			}
			return nil
		}
		rel := filepath.ToSlash(path)
		if !strings.HasSuffix(rel, ".go") || isGeneratedOrTest(rel) {
			return nil
		}
		content, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			return readErr
		}
		if !auditInsertPattern.Match(content) {
			return nil
		}
		seen[rel] = true
		if _, ok := auditWriteAllowlist[rel]; !ok {
			offenders = append(offenders, rel)
		}
		return nil
	})
	return offenders, seen, err
}

// TestAuditWritesStayOnSanctionedPaths is the structural gate REQ-050/TASK-097
// asks for: a new direct INSERT INTO audit_events fails the build unless it is
// registered in auditWriteAllowlist, and the transactional writers must call
// store.SanitizeAuditEvent so no unsanitized text reaches the trail.
func TestAuditWritesStayOnSanctionedPaths(t *testing.T) {
	root := repoRoot(t)
	offenders, seen, err := scanAuditWrites(os.DirFS(root))
	require.NoError(t, err)

	assert.Emptyf(t, offenders,
		"direct audit_events writes must be registered in auditWriteAllowlist with a reason: %v", offenders)
	for rel, reason := range auditWriteAllowlist {
		assert.Truef(t, seen[rel], "allowlisted %s no longer writes audit_events; remove the stale entry: %s", rel, reason)
	}
	for _, rel := range sanitizeRequiredFiles {
		content, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		require.NoError(t, readErr)
		assert.Containsf(t, string(content), "store.SanitizeAuditEvent",
			"%s writes audit_events transactionally and must redact through store.SanitizeAuditEvent", rel)
	}
}

// TestAuditWriteGateRejectsANewBypass is the gate's negative control: the scanner
// must report an unregistered writer, must accept a registered one, and must
// catch the idempotent spelling as well.
func TestAuditWriteGateRejectsANewBypass(t *testing.T) {
	synthetic := fstest.MapFS{
		"internal/orchestrator/new_writer.go": &fstest.MapFile{
			Data: []byte("INSERT INTO audit_events (id) VALUES (?)"),
		},
		"internal/store/sqlite/audit.go": &fstest.MapFile{
			Data: []byte("INSERT OR IGNORE INTO audit_events (id) VALUES (?)"),
		},
		"internal/store/sqlite/audit_test.go": &fstest.MapFile{
			Data: []byte("INSERT INTO audit_events (id) VALUES (?)"),
		},
	}

	offenders, seen, err := scanAuditWrites(synthetic)
	require.NoError(t, err)
	assert.Equal(t, []string{"internal/orchestrator/new_writer.go"}, offenders)
	assert.True(t, seen["internal/store/sqlite/audit.go"], "a registered writer is seen, not reported")
	assert.False(t, seen["internal/store/sqlite/audit_test.go"], "test files are out of scope")
}

func isGeneratedOrTest(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	return strings.HasPrefix(rel, "api/gen/") || strings.Contains(rel, "/gen/")
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
