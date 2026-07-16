// Package sdkcheck provides a static analyzer that detects os/exec calls
// to Helm and kubectl binaries, enforcing the SDK-only policy.
//
// REQ-037: SDK-only static quality gate — prevents runtime CLI code from
// entering the repository before any Helm business logic is implemented.
package sdkcheck

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"time"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"gopkg.in/yaml.v3"
)

// Forbidden binaries that must not be invoked via os/exec.
var forbiddenBinaries = map[string]string{
	"helm":      "helm",
	"kubectl":   "kubectl",
	"istioctl":  "istioctl",
	"argocd":    "argocd",
	"flux":      "flux",
	"terraform": "terraform",
	"tofu":      "tofu",
}

// RuleID enumerates the detection rules.
type RuleID string

const (
	RuleOSExecImport           RuleID = "os_exec_import"
	RuleForkExec               RuleID = "fork_exec"
	RuleShellWrapper           RuleID = "shell_wrapper"
	RuleForbiddenBinary        RuleID = "forbidden_binary_invocation"
	RuleExpiredException       RuleID = "expired_exception"
)

// Exception represents an allowed exception to the SDK-only rule.
type Exception struct {
	Owner     string `yaml:"owner"`
	Reason    string `yaml:"reason"`
	ExpiresAt string `yaml:"expires_at"` // YYYY-MM-DD
	Path      string `yaml:"path"`        // file path pattern (doublestar)
	Rule      string `yaml:"rule"`        // rule ID to suppress
}

// ExceptionsFile is the top-level structure of the exceptions YAML.
type ExceptionsFile struct {
	Version    string      `yaml:"version"`
	Exceptions []Exception `yaml:"exceptions"`
}

// LoadExceptions reads and validates an exceptions YAML file.
func LoadExceptions(path string) ([]Exception, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no exceptions is valid
		}
		return nil, fmt.Errorf("read exceptions: %w", err)
	}
	var ef ExceptionsFile
	if err := yaml.Unmarshal(data, &ef); err != nil {
		return nil, fmt.Errorf("parse exceptions: %w", err)
	}
	if ef.Version == "" {
		return nil, fmt.Errorf("exceptions file missing version field")
	}
	return ef.Exceptions, nil
}

// isExpired checks whether an exception has passed its expiry date.
func isExpired(expiresAt string) bool {
	if expiresAt == "" {
		return false // never expires
	}
	t, err := time.Parse("2006-01-02", expiresAt)
	if err != nil {
		return true // unparseable = treat as expired
	}
	return time.Now().After(t)
}

// NewAnalyzer creates the SDK-only static gate analyzer.
// exceptionsPath may be empty; the analyzer will run without exceptions.
func NewAnalyzer(exceptionsPath string) (*analysis.Analyzer, error) {
	exceptions, err := LoadExceptions(exceptionsPath)
	if err != nil {
		return nil, fmt.Errorf("sdkcheck: %w", err)
	}

	// Index exceptions by file path for O(1) lookup.
	exceptionMap := make(map[string][]Exception)
	for _, ex := range exceptions {
		exceptionMap[ex.Path] = append(exceptionMap[ex.Path], ex)
	}

	return &analysis.Analyzer{
		Name:     "sdkcheck",
		Doc:      "detects os/exec calls to Helm/kubectl/istioctl enforcing SDK-only policy (REQ-037)",
		Requires: []*analysis.Analyzer{inspect.Analyzer},
		Run: func(pass *analysis.Pass) (interface{}, error) {
			runAnalyzer(pass, exceptionMap)
			return nil, nil
		},
	}, nil
}

// runAnalyzer performs the AST inspection for a single package.
func runAnalyzer(pass *analysis.Pass, exceptionMap map[string][]Exception) {
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		panic("sdkcheck: inspect.Analyzer result not available")
	}
	nodeFilter := []ast.Node{
		(*ast.ImportSpec)(nil),
		(*ast.CallExpr)(nil),
	}

	insp.WithStack(nodeFilter, func(n ast.Node, push bool, stack []ast.Node) bool {
		if !push {
			return true
		}
		switch node := n.(type) {
		case *ast.ImportSpec:
			checkOSExecImport(pass, node, exceptionMap)
		case *ast.CallExpr:
			checkForbiddenCall(pass, node, stack, exceptionMap)
		}
		return true
	})
}

// checkOSExecImport flags bare os/exec imports (RuleOSExecImport).
func checkOSExecImport(pass *analysis.Pass, imp *ast.ImportSpec, exceptionMap map[string][]Exception) {
	path := importPath(imp)
	if path != "os/exec" {
		return
	}
	file := pass.Fset.File(imp.Pos()).Name()
	if hasException(exceptionMap, file, string(RuleOSExecImport)) {
		return
	}
	pass.Report(analysis.Diagnostic{
		Pos:     imp.Pos(),
		Message: fmt.Sprintf("[%s] import of os/exec is forbidden; use Go SDK (helm.sh/helm/v3/pkg/action, client-go) instead", RuleOSExecImport),
	})
}

// checkForbiddenCall detects exec.Command("helm"|"kubectl"|...) and shell wrappers.
func checkForbiddenCall(pass *analysis.Pass, call *ast.CallExpr, stack []ast.Node, exceptionMap map[string][]Exception) {
	file := pass.Fset.File(call.Pos()).Name()

	// Match: exec.Command("helm", ...) or similar
	if isExecCommand(call, pass.TypesInfo) {
		bin := extractBinaryName(call)
		if _, forbidden := forbiddenBinaries[bin]; forbidden {
			if hasException(exceptionMap, file, string(RuleForbiddenBinary)) {
				return
			}
			pass.Report(analysis.Diagnostic{
				Pos:     call.Pos(),
				Message: fmt.Sprintf("[%s] exec.Command(%q, ...) is forbidden; use Go SDK instead", RuleForbiddenBinary, bin),
			})
			return
		}
	}

	// Check for hidden shell wrappers: functions that call exec.Command with
	// "sh" or "bash" and pass a command string that references forbidden binaries.
	if isShellWrapper(pass, stack, call, pass.TypesInfo) {
		if hasException(exceptionMap, file, string(RuleShellWrapper)) {
			return
		}
		pass.Report(analysis.Diagnostic{
			Pos:     call.Pos(),
			Message: fmt.Sprintf("[%s] potential shell wrapper: %s", RuleShellWrapper, describeCaller(stack)),
		})
	}
}

// isExecCommand checks if the call is exec.Command(...).
func isExecCommand(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if pkg.Name != "exec" || sel.Sel.Name != "Command" {
		return false
	}
	// Confirm the package object is os/exec.
	if obj, ok := info.Uses[pkg]; ok {
		if pkgObj, ok := obj.(*types.PkgName); ok {
			return pkgObj.Imported().Path() == "os/exec"
		}
	}
	return false
}

// extractBinaryName extracts the first argument from exec.Command("binary", ...).
func extractBinaryName(call *ast.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind.String() != "STRING" {
		return ""
	}
	// Strip surrounding quotes.
	s := lit.Value
	if len(s) >= 2 {
		return s[1 : len(s)-1]
	}
	return s
}

// isShellWrapper checks if any ancestor function in the call stack
// ultimately wraps an exec.Command("sh"/"bash") with a forbidden binary in the args.
func isShellWrapper(pass *analysis.Pass, stack []ast.Node, call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "exec" || sel.Sel.Name != "Command" {
		return false
	}
	if obj, ok := info.Uses[pkg]; ok {
		if pkgObj, ok := obj.(*types.PkgName); ok {
			if pkgObj.Imported().Path() != "os/exec" {
				return false
			}
		}
	}

	bin := extractBinaryName(call)
	if bin != "sh" && bin != "bash" {
		return false
	}
	// Check remaining args for forbidden binary references.
	for i := 1; i < len(call.Args); i++ {
		lit, ok := call.Args[i].(*ast.BasicLit)
		if !ok || lit.Kind.String() != "STRING" {
			continue
		}
		s := lit.Value
		if len(s) < 2 {
			continue
		}
		s = s[1 : len(s)-1] // unquote
		for _, fb := range forbiddenBinaries {
			if containsWord(s, fb) {
				return true
			}
		}
	}
	return false
}

// describeCaller builds a human-readable function name from the call stack.
func describeCaller(stack []ast.Node) string {
	// Walk stack from the top (most recent) to find enclosing function.
	for i := len(stack) - 1; i >= 0; i-- {
		if fd, ok := stack[i].(*ast.FuncDecl); ok {
			return fmt.Sprintf("func %s calls exec.Command", fd.Name.Name)
		}
		if fl, ok := stack[i].(*ast.FuncLit); ok {
			return fmt.Sprintf("anonymous func at %v calls exec.Command", fl.Pos())
		}
	}
	return "exec.Command call"
}

// containsWord checks whether s contains word as a standalone token
// (not as a substring of a larger word).
func containsWord(s, word string) bool {
	// Simple check: the word appears with word boundaries.
	for i := 0; i <= len(s)-len(word); i++ {
		if s[i:i+len(word)] == word {
			before := i == 0 || isBoundary(s[i-1])
			after := i+len(word) == len(s) || isBoundary(s[i+len(word)])
			if before && after {
				return true
			}
		}
	}
	return false
}

func isBoundary(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == ';' || b == '|' || b == '&' || b == '"' || b == '\''
}

// importPath extracts the import path string from an ImportSpec.
func importPath(is *ast.ImportSpec) string {
	if is.Path == nil {
		return ""
	}
	val := is.Path.Value
	if len(val) >= 2 {
		return val[1 : len(val)-1]
	}
	return val
}

// hasException checks whether the given file+rule has a valid (non-expired) exception.
func hasException(exceptionMap map[string][]Exception, file, rule string) bool {
	// Check exact path match.
	if exs, ok := exceptionMap[file]; ok {
		for _, ex := range exs {
			if ex.Rule == rule && !isExpired(ex.ExpiresAt) {
				return true
			}
		}
	}
	// Also check glob patterns (basic prefix/suffix matching).
	for pattern, exs := range exceptionMap {
		if matchSimplePattern(pattern, file) {
			for _, ex := range exs {
				if ex.Rule == rule && !isExpired(ex.ExpiresAt) {
					return true
				}
			}
		}
	}
	return false
}

// matchSimplePattern does basic glob matching without importing doublestar.
// Supports * as a wildcard and ** for recursive matching.
func matchSimplePattern(pattern, path string) bool {
	if pattern == path {
		return true
	}
	// Simple suffix match: "*.go" matches "foo.go"
	if len(pattern) > 1 && pattern[0] == '*' && !stringsContains(path[:len(path)-len(pattern)+1], "/") {
		return len(path) >= len(pattern)-1 && path[len(path)-len(pattern)+1:] == pattern[1:]
	}
	return false
}

func stringsContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
