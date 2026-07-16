package sdkcheck

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer_ExecCommandHelm(t *testing.T) {
	// AC-037-01: Given exec.Command("helm"), When CI, Then 失败并定位
	testdata := filepath.Join("testdata", "exec_helm")
	a, err := NewAnalyzer("")
	if err != nil {
		t.Fatal(err)
	}
	results := analysistest.Run(t, testdata, a, "exec_helm")
	if len(results) == 0 {
		t.Error("expected diagnostic for exec.Command(\"helm\")")
	}
	for _, r := range results {
		if r.Pass.Analyzer.Name != "sdkcheck" {
			continue
		}
		if len(r.Diagnostics) == 0 {
			t.Error("expected diagnostics, got none")
		}
		for _, d := range r.Diagnostics {
			if d.Message == "" {
				t.Error("diagnostic message is empty")
			}
			t.Logf("diagnostic: %s at %v", d.Message, d.Pos)
		}
	}
}

func TestAnalyzer_ShellWrapper(t *testing.T) {
	// AC-037-02: Given 隐藏 shell wrapper, When analyzer, Then 规则命中
	testdata := filepath.Join("testdata", "shell_wrapper")
	a, err := NewAnalyzer("")
	if err != nil {
		t.Fatal(err)
	}
	results := analysistest.Run(t, testdata, a, "shell_wrapper")
	found := false
	for _, r := range results {
		if r.Pass.Analyzer.Name != "sdkcheck" {
			continue
		}
		for _, d := range r.Diagnostics {
			if containsRule(d.Message, string(RuleShellWrapper)) {
				found = true
			}
			t.Logf("diagnostic: %s", d.Message)
		}
	}
	if !found {
		t.Error("expected shell_wrapper diagnostic, got none")
	}
}

func TestAnalyzer_LegitimateSDK(t *testing.T) {
	// AC-037-03: Given 合法 SDK client, When CI, Then 不误报
	testdata := filepath.Join("testdata", "legitimate_sdk")
	a, err := NewAnalyzer("")
	if err != nil {
		t.Fatal(err)
	}
	results := analysistest.Run(t, testdata, a, "legitimate_sdk")
	for _, r := range results {
		if r.Pass.Analyzer.Name != "sdkcheck" {
			continue
		}
		if len(r.Diagnostics) > 0 {
			for _, d := range r.Diagnostics {
				t.Errorf("unexpected diagnostic in legitimate SDK package: %s", d.Message)
			}
		}
	}
}

func TestAnalyzer_ExpiredException(t *testing.T) {
	// AC-037-04: Given 过期例外, When CI, Then 失败
	testdata := filepath.Join("testdata", "expired_exception")
	exceptionsPath := filepath.Join(testdata, "sdkcheck.exceptions.yaml")
	a, err := NewAnalyzer(exceptionsPath)
	if err != nil {
		t.Fatal(err)
	}
	results := analysistest.Run(t, testdata, a, "expired_exception")
	found := false
	for _, r := range results {
		if r.Pass.Analyzer.Name != "sdkcheck" {
			continue
		}
		for _, d := range r.Diagnostics {
			if containsRule(d.Message, string(RuleForbiddenBinary)) {
				found = true
			}
			t.Logf("diagnostic: %s", d.Message)
		}
	}
	if !found {
		t.Error("expected forbidden_binary_invocation diagnostic for expired exception, got none")
	}
}

func TestLoadExceptions_Valid(t *testing.T) {
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
	exs, err := LoadExceptions(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(exs) != 1 {
		t.Fatalf("expected 1 exception, got %d", len(exs))
	}
	if exs[0].Owner != "ci-team" {
		t.Errorf("expected owner 'ci-team', got %q", exs[0].Owner)
	}
}

func TestLoadExceptions_Expired(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "exceptions.yaml")
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
	exs, err := LoadExceptions(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(exs) != 1 {
		t.Fatalf("expected 1 exception, got %d", len(exs))
	}
	if !isExpired(exs[0].ExpiresAt) {
		t.Error("expected exception to be expired")
	}
}

func TestLoadExceptions_Missing(t *testing.T) {
	exs, err := LoadExceptions("/nonexistent/path.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if exs != nil {
		t.Error("expected nil exceptions for missing file")
	}
}

func TestLoadExceptions_InvalidYAML(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.yaml")
	if err := os.WriteFile(path, []byte("not: [valid: yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadExceptions(path)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func containsRule(msg, rule string) bool {
	return len(msg) >= len(rule)+2 && msg[0] == '[' && msg[1:len(rule)+1] == rule
}
