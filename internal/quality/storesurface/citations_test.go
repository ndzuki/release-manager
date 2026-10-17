package storesurface

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// citationFixture writes a small tree and returns its root: the citation rules are about
// "does the cited line really show this", so a real file is the honest input.
func citationFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func findingsText(findings []string) string { return strings.Join(findings, "\n") }

func TestValidateCitationsAcceptsPointerThatShowsItsEvidence(t *testing.T) {
	root := citationFixture(t, map[string]string{
		"internal/store/sqlite/emergency_store_test.go": "one\ntwo\n\tst.EmergencyIntents().ResolveEmergencyEffect(ctx)\nfour\n",
	})
	exceptions := []Exception{{
		Interface: "EmergencyIntentStore", Method: "ResolveEmergencyEffect",
		Reason: "保留：命中 1 处 = sqlite/emergency_store_test.go:3，全部为测试",
		Citations: []Citation{{
			File: "internal/store/sqlite/emergency_store_test.go", Line: 3,
			Contains: ".ResolveEmergencyEffect(",
		}},
	}}
	if findings := validateCitations(exceptions, root); len(findings) != 0 {
		t.Fatalf("a matching citation must pass, got %#v", findings)
	}
}

// The regression TASK-328 is about: the line number drifted but the reason (and the content)
// still look right. Line existence alone would pass this; the content check must not.
func TestValidateCitationsRejectsDriftedLineNumber(t *testing.T) {
	root := citationFixture(t, map[string]string{
		"internal/store/sqlite/emergency_store_test.go": "one\ntwo\nthree\n\tst.EmergencyIntents().ResolveEmergencyEffect(ctx)\n",
	})
	exceptions := []Exception{{
		Interface: "EmergencyIntentStore", Method: "ResolveEmergencyEffect",
		Reason: "命中 1 处 = sqlite/emergency_store_test.go:1",
		Citations: []Citation{{
			File: "internal/store/sqlite/emergency_store_test.go", Line: 1,
			Contains: ".ResolveEmergencyEffect(",
		}},
	}}
	findings := validateCitations(exceptions, root)
	if !strings.Contains(findingsText(findings), "does not contain") {
		t.Fatalf("a citation pointing off its evidence must fail, got %#v", findings)
	}
}

// The reason is the human-readable copy of the same pointer: editing only the citation must
// not leave the prose pointing at an old line.
func TestValidateCitationsRejectsReasonThatDoesNotNameTheLine(t *testing.T) {
	root := citationFixture(t, map[string]string{
		"internal/audit/archiver.go": "a\nb\nDeleteByIDs x\n",
	})
	exceptions := []Exception{{
		Interface: "AuditEventStore", Method: "DeleteByIDs",
		Reason: "同为 archiver.go:99 的本地窄接口调用",
		Citations: []Citation{{
			File: "internal/audit/archiver.go", Line: 3, Contains: "DeleteByIDs",
		}},
	}}
	findings := validateCitations(exceptions, root)
	if !strings.Contains(findingsText(findings), "does not mention line 3") {
		t.Fatalf("a citation the reason does not name must fail, got %#v", findings)
	}
}

// The other direction: a fresh file:line in the prose with nothing declared would be trusted
// blindly, which is the hole this check closes.
func TestValidateCitationsRejectsUndeclaredProseReference(t *testing.T) {
	root := citationFixture(t, map[string]string{
		"internal/audit/archiver.go": "a\nb\nDeleteByIDs x\n",
	})
	exceptions := []Exception{{
		Interface: "AuditEventStore", Method: "DeleteByIDs",
		Reason: "同为 archiver.go:3 的本地窄接口调用",
	}}
	findings := validateCitations(exceptions, root)
	if !strings.Contains(findingsText(findings), "no citation declares that line") {
		t.Fatalf("an undeclared prose reference must fail, got %#v", findings)
	}
}

// An abbreviated prose reference (`archiver.go:124`) is matched by basename, which is how a
// reader reads it; the declaration carries the full path.
func TestValidateCitationsMatchesAbbreviatedReasonReference(t *testing.T) {
	root := citationFixture(t, map[string]string{
		"internal/audit/archiver.go": strings.Repeat("x\n", 123) + "a.DeleteByIDs(ctx)\n",
	})
	exceptions := []Exception{{
		Interface: "AuditEventStore", Method: "DeleteByIDs",
		Reason: "同为 internal/audit/archiver.go 的本地窄接口调用（archiver.go:124）",
		Citations: []Citation{{
			File: "internal/audit/archiver.go", Line: 124, Contains: "DeleteByIDs",
		}},
	}}
	if findings := validateCitations(exceptions, root); len(findings) != 0 {
		t.Fatalf("an abbreviated reference must match its declaration, got %#v", findings)
	}
}

// Knowledge-base references cannot be verified from here: requiring a citation for them would
// demand an entry that can never pass.
func TestValidateCitationsIgnoresKnowledgeBaseReferences(t *testing.T) {
	exceptions := []Exception{{
		Interface: "RolloutTrackingStore", Method: "Get",
		Reason: "REQ-021 只定义 rollout 表+写入、Notes/decisions/ADR-013.md:7 不定义发布",
	}}
	if findings := validateCitations(exceptions, t.TempDir()); len(findings) != 0 {
		t.Fatalf("knowledge-base references must not be required, got %#v", findings)
	}
}

func TestValidateCitationsRejectsUnusablePointers(t *testing.T) {
	root := citationFixture(t, map[string]string{"a.go": "package a\n"})
	tests := []struct {
		name        string
		exception   Exception
		wantFinding string
	}{
		{
			name: "empty file",
			exception: Exception{Interface: "A", Method: "M", Reason: "anything",
				Citations: []Citation{{Line: 1, Contains: "x"}}},
			wantFinding: "citation with an empty file",
		},
		{
			name: "empty contains",
			exception: Exception{Interface: "B", Method: "M2", Reason: "anything",
				Citations: []Citation{{File: "a.go", Line: 1}}},
			wantFinding: "empty contains",
		},
		{
			name: "missing file",
			exception: Exception{Interface: "C", Method: "M3", Reason: "anything a.go:1",
				Citations: []Citation{{File: "missing.go", Line: 1, Contains: "x"}}},
			wantFinding: "cannot be read",
		},
		{
			name: "line outside the file",
			exception: Exception{Interface: "D", Method: "M4", Reason: "anything a.go:99",
				Citations: []Citation{{File: "a.go", Line: 99, Contains: "x"}}},
			wantFinding: "is outside the file",
		},
		{
			name: "path escaping the repository",
			exception: Exception{Interface: "E", Method: "M5", Reason: "anything a.go:1",
				Citations: []Citation{{File: "../a.go", Line: 1, Contains: "x"}}},
			wantFinding: "must be repository-relative",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			findings := validateCitations([]Exception{test.exception}, root)
			if !strings.Contains(findingsText(findings), test.wantFinding) {
				t.Fatalf("want a finding containing %q, got %#v", test.wantFinding, findings)
			}
		})
	}
}

func TestContainsNumberNeedsAWholeNumber(t *testing.T) {
	if !containsNumber("见 :2685 的调用点", 2685) {
		t.Fatal("2685 must match")
	}
	if containsNumber("见 :12685 的调用点", 2685) {
		t.Fatal("2685 must not match the digits inside 12685")
	}
	if containsNumber("见 :26850 的调用点", 2685) {
		t.Fatal("2685 must not match a longer number's prefix")
	}
}

func TestCitationLinesIgnoresTrailingNewline(t *testing.T) {
	if got := len(citationLines([]byte("one\ntwo\n"))); got != 2 {
		t.Fatalf("want 2 lines, got %d", got)
	}
	if got := len(citationLines(nil)); got != 0 {
		t.Fatalf("want 0 lines for empty content, got %d", got)
	}
}
