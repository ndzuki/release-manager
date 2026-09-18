// Package docscheck implements the symbol-proximity audit for code citations in
// documentation: when the sentence next to a `file:line` citation names a
// symbol, the symbol should appear near the cited line.
//
// Why this is an audit and not (yet) a gate: measured against this repository,
// the rule's false-positive rate is high enough that making it blocking would
// drown real drift in noise. TASK-113 measured the same heuristic on the same
// corpus and found that 123 of 135 remaining candidates were false positives
// (91%), in three classes this package cannot distinguish:
//
//   - prose that names a symbol as ABSENT ("no ReplaceAttr redaction"), where the
//     cited range correctly does not contain it;
//   - prose that names the CALLER rather than the definition;
//   - shorthand chains, where the backticked span next to a citation belongs to a
//     different citation on the same line.
//
// The third class is partly mitigated here (a span adjacent to another citation
// is skipped), and non-symbol anchors are filtered out, but the first two are
// semantic and need a human. TASK-112's own assumption anticipated this: the
// fallback is a read-only mode whose findings go to a human, which is what
// `make audit-citations` is.
package docscheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Window is how many lines either side of a citation a named symbol may sit.
const Window = 5

// Finding is one citation whose neighbouring symbol is not near the cited line.
type Finding struct {
	Doc    string
	Line   int
	Target string
	Symbol string
	Detail string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: citation %s names %q, which is not within %d lines (%s)",
		f.Doc, f.Line, f.Target, f.Symbol, Window, f.Detail)
}

// Result summarises an audit run.
type Result struct {
	Files     int
	Citations int
	Checked   int
	Findings  []Finding
}

var (
	citation = regexp.MustCompile("`([A-Za-z0-9_./-]+\\.(?:go|proto|sql|vue|ts|yaml|yml|sh|md|json))(?::(\\d+)(?:-(\\d+))?)?`")
	span     = regexp.MustCompile("`([^`]+)`")
	// Strong symbols only: a multi-hump CamelCase name, a dotted qualified name,
	// or a snake_case name with at least two underscores. Single capitalised words
	// ("Run", "Push") and YAML keys are the noise TASK-113 already rejected.
	strongDotted = regexp.MustCompile(`\b[A-Z][A-Za-z0-9_]*(?:\.[A-Z][A-Za-z0-9_]*)+\b`)
	strongCamel  = regexp.MustCompile(`\b[A-Z][a-z0-9]+(?:[A-Z][a-z0-9]+)+\b`)
	strongSnake  = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+){2,}\b`)
	repoRoot     = regexp.MustCompile(`^\.?/?(cmd|internal|api|web|docs|deploy|migrations|scripts|test|configs|\.github)(/|$)`)
)

var extensions = []string{".go", ".proto", ".sql", ".md", ".ts", ".vue", ".yaml", ".yml", ".sh", ".json"}

// Audit scans the markdown files under docs and reports findings.
func Audit(root string) (*Result, error) {
	tree, err := repoFiles(root)
	if err != nil {
		return nil, err
	}
	result := &Result{}
	for _, doc := range tree.markdown {
		if err := auditFile(root, doc, tree, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

type fileTree struct {
	all      map[string]bool
	markdown []string
	lines    map[string][]string
}

func repoFiles(root string) (*fileTree, error) {
	t := &fileTree{all: map[string]bool{}, lines: map[string][]string{}}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "gen", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			t.markdown = append(t.markdown, rel)
		}
		t.all[rel] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(t.markdown)
	return t, nil
}

func (t *fileTree) linesOf(root, rel string) []string {
	if cached, ok := t.lines[rel]; ok {
		return cached
	}
	raw, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // trusted local checkout
	if err != nil {
		t.lines[rel] = nil
		return nil
	}
	lines := strings.Split(string(raw), "\n")
	t.lines[rel] = lines
	return lines
}

func auditFile(root, doc string, tree *fileTree, result *Result) error {
	raw, err := os.ReadFile(filepath.Join(root, doc)) //nolint:gosec // trusted local checkout
	if err != nil {
		return err
	}
	result.Files++
	lines := strings.Split(string(raw), "\n")
	for index, line := range lines {
		if strings.Contains(line, "check-docs:ignore") {
			continue
		}
		allCitations := citation.FindAllStringSubmatchIndex(line, -1)
		for _, match := range allCitations {
			target := line[match[2]:match[3]]
			// The line number is an optional group, so its indices are -1 when the
			// citation is a bare path.
			if match[4] < 0 || match[5] < 0 {
				continue
			}
			number := line[match[4]:match[5]]
			if number == "" || !repoRoot.MatchString(target) {
				continue
			}
			result.Citations++
			cited, resolveErr := resolve(target, tree)
			if resolveErr != nil {
				continue
			}
			symbols := neighbourSymbols(line, match[0], match[1], allCitations)
			if len(symbols) == 0 {
				continue
			}
			result.Checked++
			at := atoi(number)
			body := tree.linesOf(root, cited)
			for _, symbol := range symbols {
				if withinWindow(body, symbol, at) {
					continue
				}
				detail := "symbol is absent from the file"
				if where := firstLineWith(body, symbol); where > 0 {
					detail = fmt.Sprintf("symbol is at line %d", where)
				}
				result.Findings = append(result.Findings, Finding{
					Doc: doc, Line: index + 1, Target: target + ":" + number, Symbol: symbol, Detail: detail,
				})
			}
		}
	}
	return nil
}

// neighbourSymbols returns the strong symbols from the backticked span closest to
// the citation. A span whose nearest citation is a DIFFERENT one on the same line
// is skipped: that is the shorthand-chain false positive TASK-113 documented
// (`path:84-135` followed by `:157`, where the second span belongs to the second
// citation).
func neighbourSymbols(line string, start, end int, citations [][]int) []string {
	type candidate struct {
		start, end int
		text       string
	}
	var spans []candidate
	for _, match := range span.FindAllStringSubmatchIndex(line, -1) {
		text := line[match[2]:match[3]]
		if citation.MatchString("`" + text + "`") {
			continue
		}
		// In a shorthand chain (`path:84-135`、`Sym`（`path:157`)) the span sits
		// right after an earlier citation and belongs to the later one, so a span
		// preceded by another citation is not this citation's anchor.
		if precededByCitation(match[0], citations) {
			continue
		}
		spans = append(spans, candidate{start: match[0], end: match[1], text: text})
	}
	var before, after *candidate
	for i := range spans {
		if spans[i].end <= start {
			if before == nil || spans[i].end > before.end {
				before = &spans[i]
			}
		}
		if spans[i].start >= end {
			if after == nil || spans[i].start < after.start {
				after = &spans[i]
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, pick := range []*candidate{before, after} {
		if pick == nil {
			continue
		}
		for _, symbol := range strongSymbols(pick.text) {
			if !seen[symbol] {
				seen[symbol] = true
				out = append(out, symbol)
			}
		}
	}
	return out
}

func strongSymbols(text string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{strongDotted, strongCamel, strongSnake} {
		out = append(out, re.FindAllString(text, -1)...)
	}
	var filtered []string
	for _, symbol := range out {
		skip := false
		for _, ext := range extensions {
			if strings.HasSuffix(symbol, ext) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, symbol)
		}
	}
	return filtered
}

func withinWindow(lines []string, symbol string, at int) bool {
	low, high := at-Window, at+Window
	if low < 1 {
		low = 1
	}
	if high > len(lines) {
		high = len(lines)
	}
	for i := low; i <= high; i++ {
		if strings.Contains(lines[i-1], symbol) {
			return true
		}
	}
	return false
}

func firstLineWith(lines []string, symbol string) int {
	for i, line := range lines {
		if strings.Contains(line, symbol) {
			return i + 1
		}
	}
	return 0
}

func resolve(target string, tree *fileTree) (string, error) {
	if tree.all[target] {
		return target, nil
	}
	var matches []string
	for path := range tree.all {
		if strings.HasSuffix(path, "/"+target) {
			matches = append(matches, path)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("ambiguous or unknown target %q", target)
	}
	return matches[0], nil
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// precededByCitation reports whether another citation ends just before this span,
// separated only by punctuation such as 、 or ，.
func precededByCitation(spanStart int, citations [][]int) bool {
	const gap = 3
	for _, match := range citations {
		if match[1] <= spanStart && spanStart-match[1] <= gap {
			return true
		}
	}
	return false
}
