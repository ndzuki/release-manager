// Package errcodes checks that every stable error code a requirement asserts in
// its acceptance criteria is actually emittable by the implementation.
//
// A requirement names its domain error codes twice: in the code column of its
// error model table, and in the acceptance criteria that assert the outcome.
// Their intersection is the contract a client can rely on, and this gate exists
// because that contract rots silently. The six defects fixed in TASK-121 through
// TASK-124 all had the code declared while the implementation returned only a
// prose message, and no existing gate (lint, check-docs, lint-proto, licenses,
// sdk-check) could see it.
//
// Scope, and why it is deliberately narrow: only codes that appear in BOTH the
// error model table and an AC line are checked. Widening to every snake_case
// token in an AC line was measured to be mostly noise, because AC text also
// backticks field names (`payload_sha256`, `status_filter`, `e2e_run_id`). A code
// that the error model names but no AC asserts is an error-model/AC coverage
// mismatch, not a code defect, and is out of scope here.
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

// Violation reports one asserted code with no emitter.
type Violation struct {
	REQ    string
	Code   string
	Detail string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: error code %q is asserted by an AC but has no emitter (%s)", v.REQ, v.Code, v.Detail)
}

// Result summarises one requirement document.
type Result struct {
	REQ          string
	CodesChecked int
	Violations   []Violation
}

// Options configures a check run.
type Options struct {
	// RepoRoot is searched for emitters (Go string literals and shell scripts).
	RepoRoot string
	// Exceptions maps a code to the reason it is allowed to be unemitted.
	Exceptions map[string]string
	// Vocabulary is the canonical emergency reason-code vocabulary (short names
	// <X> of EMERGENCY_REASON_CODE_<X>). The caller derives it from the whole
	// requirement set, because the contract REQ that declares it (REQ-079) is a
	// different document from the one being checked. When empty, the document
	// under check supplies its own vocabulary, which keeps single-document
	// callers and tests working.
	Vocabulary map[string]struct{}
}

// acLine matches an acceptance-criteria bullet, with or without a checkbox and
// with or without bold around the AC id.
var acLine = regexp.MustCompile(`^\s*-\s*\[[ x]\]\s*\*{0,2}AC-`)

// backticked matches a snake_case token inside backticks.
var backticked = regexp.MustCompile("`([a-z][a-z0-9]*(?:_[a-z0-9]+)+)`")

// tableCode matches the first column of a markdown table row when it is a
// backticked snake_case identifier -- the shape a REQ uses for its error codes.
var tableCode = regexp.MustCompile("^\\|\\s*`([a-z][a-z0-9]*(?:_[a-z0-9]+)+)`\\s*\\|")

// heading splits a document into "## " / "### " sections.
var heading = regexp.MustCompile(`(?m)^#{2,3} `)

// quotedSnake matches a quoted lowercase snake_case string literal.
var quotedSnake = regexp.MustCompile(`"([a-z][a-z0-9]*(?:_[a-z0-9]+)+)"`)

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

// Check reads one requirement document and reports the codes it asserts that no
// emitter provides.
func Check(reqPath string, opts Options) (*Result, error) {
	raw, err := os.ReadFile(reqPath)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	req := strings.TrimSuffix(filepath.Base(reqPath), ".md")

	emitted, err := emitIndex(opts.RepoRoot)
	if err != nil {
		return nil, err
	}
	// The canonical reason-code vocabulary is the set of codes the requirement
	// set names in long form; it is scoped to that vocabulary so widening cannot
	// pull in field names or prose (measured: V12 §9.2-9.3).
	vocabulary := opts.Vocabulary
	if len(vocabulary) == 0 {
		vocabulary = canonicalVocabulary(text)
	}
	emittedReasonCodes, err := reasonCodeEmitIndex(opts.RepoRoot, vocabulary)
	if err != nil {
		return nil, err
	}

	codes := assertedCodes(text)
	codes = append(codes, assertedCanonicalReasonCodes(text, vocabulary)...)
	result := &Result{REQ: req}
	for _, code := range codes {
		result.CodesChecked++
		if _, ok := emitted[code]; ok {
			continue
		}
		if _, ok := emittedReasonCodes[code]; ok {
			continue
		}
		if reason, ok := opts.Exceptions[code]; ok {
			_ = reason // the caller reports exceptions separately
			continue
		}
		result.Violations = append(result.Violations, Violation{
			REQ:    req,
			Code:   code,
			Detail: "declared in the error model and asserted by an AC",
		})
	}
	return result, nil
}

// assertedCodes returns the codes that appear in both an error-model table and
// an acceptance-criteria line, sorted.
func assertedCodes(text string) []string {
	inTable := map[string]bool{}
	for _, section := range heading.Split(text, -1) {
		if !strings.Contains(firstLine(section), "错误") {
			continue
		}
		for _, line := range strings.Split(section, "\n") {
			if m := tableCode.FindStringSubmatch(line); m != nil {
				inTable[m[1]] = true
			}
		}
	}

	inAC := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if !acLine.MatchString(line) {
			continue
		}
		for _, m := range backticked.FindAllStringSubmatch(line, -1) {
			inAC[m[1]] = true
		}
	}

	var codes []string
	for code := range inTable {
		if inAC[code] {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	return codes
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

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

// emitIndex collects every quoted lowercase snake_case literal in the repo's Go
// sources and shell scripts. Shell is included because the dev lifecycle scripts
// carry their own stable error codes (REQ-065).
func emitIndex(root string) (map[string]struct{}, error) {
	emitted := map[string]struct{}{}
	if root == "" {
		return emitted, nil
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "gen", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".sh") {
			return nil
		}
		// The walk covers a trusted local checkout (the repository, or the vault
		// passed via REQS_DIR) rather than attacker-controlled input, so the
		// symlink-race concern G122 raises does not apply to this gate.
		raw, readErr := os.ReadFile(path) //nolint:gosec // trusted local checkout, see above
		if readErr != nil {
			return readErr
		}
		for _, m := range quotedSnake.FindAllStringSubmatch(string(raw), -1) {
			emitted[m[1]] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return emitted, nil
}
