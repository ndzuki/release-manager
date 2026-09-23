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
