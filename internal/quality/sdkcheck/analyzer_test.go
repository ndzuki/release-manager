package sdkcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer_AcceptanceFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		fixture        string
		exceptionsPath string
	}{
		{name: "AC-037-01 direct Helm invocation fails with location", fixture: "exec_helm"},
		{name: "AC-037-02 shell wrapper is detected", fixture: "shell_wrapper"},
		{name: "AC-037-03 Helm SDK is accepted", fixture: "legitimate_sdk"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			testAnalyzerFixture(t, testCase.fixture, testCase.exceptionsPath)
		})
	}
}

func TestAnalyzer_ExpiredException(t *testing.T) {
	t.Parallel()

	exceptionsPath := filepath.Join("testdata", "expired_exception", "sdkcheck.exceptions.yaml")
	_, err := NewAnalyzer(exceptionsPath)
	if err == nil || !containsRule(err.Error(), string(RuleExpiredException)) {
		t.Fatalf("expected expired_exception error, got %v", err)
	}
}

func testAnalyzerFixture(t *testing.T, fixture, exceptionsPath string) {
	t.Helper()

	analyzer, err := NewAnalyzer(exceptionsPath)
	if err != nil {
		t.Fatal(err)
	}
	results := analysistest.Run(t, filepath.Join("testdata", fixture), analyzer, fixture)
	if len(results) == 0 {
		t.Fatalf("fixture %q produced no analysis result", fixture)
	}
}

func TestLoadExceptions_Valid(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "exceptions.yaml")
	data := `version: "1"
exceptions:
  - owner: "ci-team"
    reason: "approved for CI bootstrap"
    expires_at: "2099-12-31"
    path: "cmd/sdkcheck/*.go"
    rule: "os_exec_import"
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	exceptions, err := LoadExceptions(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(exceptions) != 1 || exceptions[0].Owner != "ci-team" {
		t.Fatalf("unexpected exceptions: %#v", exceptions)
	}
}

func TestLoadExceptions_RejectsInvalidEntries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data string
	}{
		{
			name: "unsupported version",
			data: "version: '2'\nexceptions: []\n",
		},
		{
			name: "missing owner",
			data: "version: '1'\nexceptions:\n- reason: x\n  expires_at: '2099-12-31'\n  path: '*.go'\n  rule: os_exec_import\n",
		},
		{
			name: "missing expiry",
			data: "version: '1'\nexceptions:\n- owner: x\n  reason: x\n  path: '*.go'\n  rule: os_exec_import\n",
		},
		{
			name: "unknown rule",
			data: "version: '1'\nexceptions:\n- owner: x\n  reason: x\n  expires_at: '2099-12-31'\n  path: '*.go'\n  rule: unknown\n",
		},
		{
			name: "invalid expiry",
			data: "version: '1'\nexceptions:\n- owner: x\n  reason: x\n  expires_at: never\n  path: '*.go'\n  rule: os_exec_import\n",
		},
		{
			name: "invalid glob",
			data: "version: '1'\nexceptions:\n- owner: x\n  reason: x\n  expires_at: '2099-12-31'\n  path: '[.go'\n  rule: os_exec_import\n",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "exceptions.yaml")
			if err := os.WriteFile(path, []byte(testCase.data), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadExceptions(path); err == nil {
				t.Fatal("LoadExceptions() succeeded for invalid entry")
			}
		})
	}
}

func TestLoadExceptions_Expired(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "exceptions.yaml")
	data := `version: "1"
exceptions:
  - owner: "dev"
    reason: "migration"
    expires_at: "2020-01-01"
    path: "foo.go"
    rule: "forbidden_binary_invocation"
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadExceptions(path)
	if err == nil || !containsRule(err.Error(), string(RuleExpiredException)) {
		t.Fatalf("expected expired exception error, got %v", err)
	}
}

func TestLoadExceptions_Missing(t *testing.T) {
	exceptions, err := LoadExceptions("/nonexistent/path.yaml")
	if err == nil || exceptions != nil {
		t.Fatalf("expected missing file error, got exceptions=%#v err=%v", exceptions, err)
	}
}

func TestLoadExceptions_InvalidYAML(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.yaml")
	if err := os.WriteFile(path, []byte("not: [valid: yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExceptions(path); err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadExceptions_EmptyPath(t *testing.T) {
	exceptions, err := LoadExceptions("")
	if err != nil || exceptions == nil {
		t.Fatalf("expected empty exception set, got exceptions=%#v err=%v", exceptions, err)
	}
}

func containsRule(message, rule string) bool {
	return strings.Contains(message, "["+rule+"]")
}
