// Canonical emergency reason codes: the vocabulary check that the lowercase
// rules in check.go structurally cannot perform.
//
// LOCKED_PATH and its siblings are uppercase, so tableCode and quotedSnake never
// match them and an AC asserting one was silently unchecked (the blind spot
// measured as V12 in Notes/Audit-2026-09-18/09-req-overturn-triage.md §9). This
// file adds a second, deliberately vocabulary-scoped path: a code is only
// considered when the requirement set names it in the long form
// EMERGENCY_REASON_CODE_<X>, which is what keeps the uppercase rule from
// re-introducing the field-name noise that widening the lowercase regexes was
// measured to produce.
//
// The blank line below keeps this a file note: the package doc stays in check.go.

package errcodes

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// canonicalReasonCode matches the long form of a canonical emergency reason
// code, EMERGENCY_REASON_CODE_<X>. The canonical vocabulary is written in this
// long form by the contract REQ (REQ-079) and by the proto enum, while a
// requirement's error-model table and its AC usually name the short form <X>.
//
// This exists because the lowercase rules above cannot see these codes at all:
// `LOCKED_PATH` is uppercase, so tableCode and quotedSnake never match it, and
// an AC that asserts it was silently unchecked (the blind spot recorded as V12
// in Notes/Audit-2026-09-18/09-req-overturn-triage.md §9).
var canonicalReasonCode = regexp.MustCompile(`\bEMERGENCY_REASON_CODE_([A-Z][A-Z0-9_]*)\b`)

// reasonCodeConstructor is the Go call that turns a canonical reason code into
// the wire error the client sees. Only a code reaching this constructor is
// EMITTED; merely naming the enum member is not emission (the same member is
// named by test assertions).
var reasonCodeConstructors = map[string]struct{}{
	"emergencyError":       {},
	"emergencyDetailError": {},
}

// enumMemberPrefix prefixes a canonical reason code inside its Go enum member
// (orchestratorv1.EmergencyReasonCode_<enumMemberPrefix><X>).
const enumMemberPrefix = "EmergencyReasonCode_EMERGENCY_REASON_CODE_"

// CanonicalVocabulary returns the union of the canonical emergency reason-code
// vocabulary named in long form across the given requirement documents. Callers
// pass every requirement document, because the contract REQ that declares the
// vocabulary is usually not the document being checked.
func CanonicalVocabulary(reqPaths []string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for _, path := range reqPaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		for _, m := range canonicalReasonCode.FindAllStringSubmatch(string(raw), -1) {
			out[m[1]] = struct{}{}
		}
	}
	return out, nil
}

// canonicalVocabulary returns the short names <X> of the canonical emergency
// reason codes that appear in long form anywhere in one requirement document.
// The contract REQ declares the whole vocabulary this way, so scoping to it
// keeps the uppercase check precise.
func canonicalVocabulary(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range canonicalReasonCode.FindAllStringSubmatch(text, -1) {
		out[m[1]] = struct{}{}
	}
	return out
}

// assertedCanonicalReasonCodes returns the canonical reason codes this document
// asserts: a code counts when both its error-model section and an AC line name
// it, in either the short or the long form. The two sides are matched
// independently because a REQ commonly writes the short form in its error-model
// table and the long form in prose (and vice versa).
func assertedCanonicalReasonCodes(text string, vocabulary map[string]struct{}) []string {
	if len(vocabulary) == 0 {
		return nil
	}
	var errorModel, ac strings.Builder
	for _, section := range heading.Split(text, -1) {
		if strings.Contains(firstLine(section), "错误") {
			errorModel.WriteString(section)
			errorModel.WriteByte('\n')
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if acLine.MatchString(line) {
			ac.WriteString(line)
			ac.WriteByte('\n')
		}
	}
	errorModelText, acText := errorModel.String(), ac.String()

	var codes []string
	for code := range vocabulary {
		short := regexp.QuoteMeta("`" + code + "`")
		long := regexp.QuoteMeta("`EMERGENCY_REASON_CODE_" + code + "`")
		if !strings.Contains(errorModelText, short) && !strings.Contains(errorModelText, long) {
			continue
		}
		if !strings.Contains(acText, short) && !strings.Contains(acText, long) {
			continue
		}
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// reasonCodeEmitIndex reports which canonical reason codes the repository can
// actually EMIT, i.e. which ones reach a reasonCodeConstructors call as a
// literal argument in a non-test Go file.
//
// Precision (deliberate, and the reason this parses rather than greps): naming
// the enum member is not emission. emergency_test.go asserts these members, and
// emergency.go:891 passes the string "EMERGENCY_REASON_CODE_" to TrimPrefix --
// both would satisfy a plain identifier reference while emitting nothing.
//
// Known limit, stated so the claim stays honest: only LITERAL enum arguments are
// seen, so a code routed through a variable into the constructor would be
// missed. Nothing in the repository does that today.
func reasonCodeEmitIndex(root string, vocabulary map[string]struct{}) (map[string]struct{}, error) {
	emitted := map[string]struct{}{}
	if root == "" || len(vocabulary) == 0 {
		return emitted, nil
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "gen", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path) //nolint:gosec // trusted local checkout, see emitIndex
		if readErr != nil {
			return readErr
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, raw, 0)
		if parseErr != nil {
			// A file the tool cannot parse must not silently shrink the emitter
			// set, so report it instead of skipping.
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		for _, call := range reasonCodeConstructorCalls(file) {
			for _, arg := range call.Args {
				if code, ok := canonicalReasonCodeArg(arg, vocabulary); ok {
					emitted[code] = struct{}{}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return emitted, nil
}

// reasonCodeConstructorCalls returns every call in file whose function is one of
// reasonCodeConstructors.
func reasonCodeConstructorCalls(file *ast.File) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		if _, ok := reasonCodeConstructors[ident.Name]; ok {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

// canonicalReasonCodeArg reports the canonical reason code of a literal enum
// argument shaped orchestratorv1.EmergencyReasonCode_EMERGENCY_REASON_CODE_<X>.
// A bare member name is accepted too, for a file in the enum's own package.
func canonicalReasonCodeArg(expr ast.Expr, vocabulary map[string]struct{}) (string, bool) {
	var name string
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		name = e.Sel.Name
	case *ast.Ident:
		name = e.Name
	default:
		return "", false
	}
	code, ok := strings.CutPrefix(name, enumMemberPrefix)
	if !ok {
		return "", false
	}
	if _, ok := vocabulary[code]; !ok {
		return "", false
	}
	return code, true
}
