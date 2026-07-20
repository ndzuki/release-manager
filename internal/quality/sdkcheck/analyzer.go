// Package sdkcheck provides a static analyzer that detects process execution
// paths which could invoke Helm, kubectl, or other forbidden command-line tools.
//
// REQ-037: SDK-only static quality gate — prevents runtime CLI code from
// entering the repository before any Helm business logic is implemented.
package sdkcheck

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"gopkg.in/yaml.v3"
)

// Forbidden binaries that must not be invoked via a process execution API.
var forbiddenBinaries = map[string]struct{}{
	"helm":      {},
	"kubectl":   {},
	"istioctl":  {},
	"argocd":    {},
	"flux":      {},
	"terraform": {},
	"tofu":      {},
}

// RuleID enumerates the detection rules.
type RuleID string

const (
	RuleOSExecImport     RuleID = "os_exec_import"
	RuleForkExec         RuleID = "fork_exec"
	RuleShellWrapper     RuleID = "shell_wrapper"
	RuleForbiddenBinary  RuleID = "forbidden_binary_invocation"
	RuleExpiredException RuleID = "expired_exception"
)

// Exception represents an allowed exception to the SDK-only rule.
type Exception struct {
	Owner     string `yaml:"owner"`
	Reason    string `yaml:"reason"`
	ExpiresAt string `yaml:"expires_at"` // YYYY-MM-DD
	Path      string `yaml:"path"`       // file path pattern (doublestar)
	Rule      string `yaml:"rule"`       // rule ID to suppress
}

// ExceptionsFile is the top-level structure of the exceptions YAML.
type ExceptionsFile struct {
	Version    string      `yaml:"version"`
	Exceptions []Exception `yaml:"exceptions"`
}

// LoadExceptions reads and validates an exceptions YAML file.
func LoadExceptions(path string) ([]Exception, error) {
	if path == "" {
		return []Exception{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("exceptions file does not exist: %w", err)
		}
		return nil, fmt.Errorf("read exceptions: %w", err)
	}

	var exceptionsFile ExceptionsFile
	if err := yaml.Unmarshal(data, &exceptionsFile); err != nil {
		return nil, fmt.Errorf("parse exceptions: %w", err)
	}
	if strings.TrimSpace(exceptionsFile.Version) == "" {
		return nil, fmt.Errorf("exceptions file missing version field")
	}
	if exceptionsFile.Version != "1" {
		return nil, fmt.Errorf("unsupported exceptions file version %q", exceptionsFile.Version)
	}

	for index, exception := range exceptionsFile.Exceptions {
		if err := validateException(exception); err != nil {
			return nil, fmt.Errorf("validate exception %d: %w", index+1, err)
		}
	}

	return exceptionsFile.Exceptions, nil
}

func validateException(exception Exception) error {
	requiredFields := []struct {
		name  string
		value string
	}{
		{name: "owner", value: exception.Owner},
		{name: "reason", value: exception.Reason},
		{name: "expires_at", value: exception.ExpiresAt},
		{name: "path", value: exception.Path},
		{name: "rule", value: exception.Rule},
	}
	for _, field := range requiredFields {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("missing %s field", field.name)
		}
	}
	if !validRule(exception.Rule) {
		return fmt.Errorf("unknown rule %q", exception.Rule)
	}
	if _, err := time.Parse(time.DateOnly, exception.ExpiresAt); err != nil {
		return fmt.Errorf("parse expires_at %q: %w", exception.ExpiresAt, err)
	}
	if isExpired(exception.ExpiresAt) {
		return fmt.Errorf("[%s] exception expired at %s", RuleExpiredException, exception.ExpiresAt)
	}
	if _, err := doublestar.Match(filepath.ToSlash(exception.Path), "sdkcheck-validation.go"); err != nil {
		return fmt.Errorf("parse path pattern %q: %w", exception.Path, err)
	}
	return nil
}

func validRule(rule string) bool {
	switch RuleID(rule) {
	case RuleOSExecImport, RuleForkExec, RuleShellWrapper, RuleForbiddenBinary:
		return true
	default:
		return false
	}
}

// isExpired checks whether an exception has passed its expiry date.
func isExpired(expiresAt string) bool {
	expiresOn, err := time.Parse(time.DateOnly, expiresAt)
	if err != nil {
		return true
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return expiresOn.Before(today)
}

// NewAnalyzer creates the SDK-only static gate analyzer.
// exceptionsPath may be empty; the analyzer will run without exceptions.
func NewAnalyzer(exceptionsPath string) (*analysis.Analyzer, error) {
	exceptions, err := LoadExceptions(exceptionsPath)
	if err != nil {
		return nil, fmt.Errorf("sdkcheck: %w", err)
	}

	exceptionMap := make(map[string][]Exception, len(exceptions))
	for _, exception := range exceptions {
		exceptionMap[exception.Path] = append(exceptionMap[exception.Path], exception)
	}

	return &analysis.Analyzer{
		Name:     "sdkcheck",
		Doc:      "detects process execution paths that violate the SDK-only policy (REQ-037)",
		Requires: []*analysis.Analyzer{inspect.Analyzer},
		Run: func(pass *analysis.Pass) (interface{}, error) {
			runAnalyzer(pass, exceptionMap)
			return nil, nil
		},
	}, nil
}

// runAnalyzer performs the AST inspection for a single package.
func runAnalyzer(pass *analysis.Pass, exceptionMap map[string][]Exception) {
	in, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		panic("sdkcheck: inspect.Analyzer result not available")
	}

	nodeFilter := []ast.Node{(*ast.ImportSpec)(nil), (*ast.CallExpr)(nil)}
	in.WithStack(nodeFilter, func(node ast.Node, push bool, stack []ast.Node) bool {
		if !push {
			return true
		}
		switch typedNode := node.(type) {
		case *ast.ImportSpec:
			checkOSExecImport(pass, typedNode, exceptionMap)
		case *ast.CallExpr:
			checkProcessLaunch(pass, typedNode, stack, exceptionMap)
		}
		return true
	})
}

// checkOSExecImport flags bare os/exec imports.
func checkOSExecImport(pass *analysis.Pass, imp *ast.ImportSpec, exceptionMap map[string][]Exception) {
	if importPath(imp) != "os/exec" {
		return
	}
	file := pass.Fset.File(imp.Pos()).Name()
	if hasException(exceptionMap, file, RuleOSExecImport) {
		return
	}
	pass.Report(analysis.Diagnostic{
		Pos:     imp.Pos(),
		Message: fmt.Sprintf("[%s] import of os/exec is forbidden; use Go SDK (helm.sh/helm/v3/pkg/action, client-go) instead", RuleOSExecImport),
	})
}

// checkProcessLaunch detects direct forbidden binaries, shell wrappers, and fork APIs.
func checkProcessLaunch(pass *analysis.Pass, call *ast.CallExpr, stack []ast.Node, exceptionMap map[string][]Exception) {
	file := pass.Fset.File(call.Pos()).Name()
	if isForkExec(call, pass.TypesInfo) {
		if !hasException(exceptionMap, file, RuleForkExec) {
			pass.Report(analysis.Diagnostic{
				Pos:     call.Pos(),
				Message: fmt.Sprintf("[%s] process fork/exec is forbidden; use the approved Go SDK instead", RuleForkExec),
			})
		}
		return
	}
	if !isExecCommand(call, pass.TypesInfo) {
		return
	}

	bin, ok := constantString(call.Args, 0, pass.TypesInfo)
	if !ok {
		return
	}
	if isForbiddenBinary(bin) {
		if hasException(exceptionMap, file, RuleForbiddenBinary) {
			return
		}
		position := pass.Fset.Position(call.Pos())
		pass.Report(analysis.Diagnostic{
			Pos:     call.Pos(),
			Message: fmt.Sprintf("[%s] exec.Command(%q, ...) is forbidden at %s; use Go SDK instead", RuleForbiddenBinary, bin, position),
		})
		return
	}
	if !isShellWrapper(call, pass.TypesInfo) || hasException(exceptionMap, file, RuleShellWrapper) {
		return
	}
	position := pass.Fset.Position(call.Pos())
	pass.Report(analysis.Diagnostic{
		Pos:     call.Pos(),
		Message: fmt.Sprintf("[%s] potential shell wrapper at %s: %s", RuleShellWrapper, position, describeCaller(stack)),
	})
}

func isExecCommand(call *ast.CallExpr, info *types.Info) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Command" {
		return false
	}
	function, ok := info.Uses[selector.Sel].(*types.Func)
	return ok && function.Pkg() != nil && function.Pkg().Path() == "os/exec"
}

func isForkExec(call *ast.CallExpr, info *types.Info) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch selector.Sel.Name {
	case "ForkExec", "Exec", "StartProcess":
	default:
		return false
	}
	function, ok := info.Uses[selector.Sel].(*types.Func)
	return ok && function.Pkg() != nil && function.Pkg().Path() == "syscall"
}

func constantString(arguments []ast.Expr, index int, info *types.Info) (string, bool) {
	if index >= len(arguments) {
		return "", false
	}
	if typeAndValue, ok := info.Types[arguments[index]]; ok && typeAndValue.Value != nil && typeAndValue.Value.Kind() == constant.String {
		return constant.StringVal(typeAndValue.Value), true
	}
	literal, ok := arguments[index].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	text, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	return text, true
}

func isForbiddenBinary(command string) bool {
	_, forbidden := forbiddenBinaries[filepath.Base(command)]
	return forbidden
}

func isShellWrapper(call *ast.CallExpr, info *types.Info) bool {
	bin, ok := constantString(call.Args, 0, info)
	if !ok {
		return false
	}
	bin = filepath.Base(bin)
	if bin != "sh" && bin != "bash" {
		return false
	}
	for index := 1; index < len(call.Args); index++ {
		argument, ok := constantString(call.Args, index, info)
		if !ok {
			continue
		}
		for forbidden := range forbiddenBinaries {
			if containsWord(argument, forbidden) {
				return true
			}
		}
	}
	return false
}

func describeCaller(stack []ast.Node) string {
	for index := len(stack) - 1; index >= 0; index-- {
		switch node := stack[index].(type) {
		case *ast.FuncDecl:
			return fmt.Sprintf("func %s calls exec.Command", node.Name.Name)
		case *ast.FuncLit:
			return fmt.Sprintf("anonymous func at %v calls exec.Command", node.Pos())
		}
	}
	return "exec.Command call"
}

func containsWord(s, word string) bool {
	for index := 0; index <= len(s)-len(word); index++ {
		if s[index:index+len(word)] != word {
			continue
		}
		before := index == 0 || isBoundary(s[index-1])
		after := index+len(word) == len(s) || isBoundary(s[index+len(word)])
		if before && after {
			return true
		}
	}
	return false
}

func isBoundary(char byte) bool {
	return strings.ContainsRune(" \t\n;|&\"'", rune(char))
}

func importPath(imp *ast.ImportSpec) string {
	if imp.Path == nil {
		return ""
	}
	value := imp.Path.Value
	if len(value) >= 2 {
		return value[1 : len(value)-1]
	}
	return value
}

func hasException(exceptionMap map[string][]Exception, file string, rule RuleID) bool {
	file = filepath.ToSlash(filepath.Clean(file))
	candidates := []string{file, filepath.Base(file)}
	parts := strings.Split(file, "/")
	for index := range parts {
		candidates = append(candidates, strings.Join(parts[index:], "/"))
	}

	for pattern, exceptions := range exceptionMap {
		pattern = filepath.ToSlash(filepath.Clean(pattern))
		for _, candidate := range candidates {
			matched, err := doublestar.Match(pattern, candidate)
			if err != nil || !matched {
				continue
			}
			for _, exception := range exceptions {
				if exception.Rule == string(rule) && !isExpired(exception.ExpiresAt) {
					return true
				}
			}
		}
	}
	return false
}
