package storesurface

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Citation is one machine-checked evidence pointer inside an exception's reason: line `line`
// of `file` must contain the `contains` substring.
//
// Why the registry carries these (TASK-328): the reasons are `文件:行号` evidence, and the
// gate used to trust them blindly. Seven citations in the shipped registry pointed 9-171
// lines past their evidence — line numbers drift on every unrelated edit above them — and
// `make check-store-surface` stayed green, because "the reason mentions a line number" was
// never checked against the line it names. A citation that is not verified is decoration.
//
// `contains` is a literal substring, not a type-aware query: the pointer's job is to say
// "this is the evidence", so "the cited line shows this text" is exactly the contract a reader
// needs. The type-based analysis this gate performs elsewhere answers a different question
// (which method objects are selected) and is untouched by this file.
type Citation struct {
	File     string `yaml:"file"`
	Line     int    `yaml:"line"`
	Contains string `yaml:"contains"`
}

// reasonCitation finds the `path:line` references a reason writes into its prose. The
// extension list keeps identifiers such as `internal/operator/ca.Load` and knowledge-base
// references such as `REQ-064:266` out of the match.
var reasonCitation = regexp.MustCompile(`([A-Za-z0-9_][A-Za-z0-9_./-]*\.(?:go|md|yaml|yml|ts|vue|json)):([0-9]+)`)

// externalRef names the knowledge-base roots, which live outside this repository (same list
// check-docs uses). A reference into them cannot be verified here, so it is not required to be
// declared — the alternative would be a citation entry that can never pass.
var externalRef = regexp.MustCompile(`^(?:Design|Notes|Requirements|Tasks|References|Projects|myNote)/`)

// validateCitations checks every declared citation and the two-way agreement between a
// reason's prose references and its declared citations. A finding makes the gate fail.
//
// Two directions, because either half can be edited alone:
//   - every declared citation must exist, fit inside its file, and the line must show
//     `contains`;
//   - the reason must name that same file (by basename) and line, and every repository
//     `file:line` the reason writes must have a declaration.
func validateCitations(exceptions []Exception, root string) []string {
	var findings []string
	for _, e := range exceptions {
		if e.Interface == "" || e.Method == "" {
			// Validate already reports the entry itself; a citation for it would be noise.
			continue
		}
		key := e.Interface + "." + e.Method
		findings = append(findings, validateEntryCitations(e, key, root)...)
	}
	return findings
}

// citationCount reports how many citations the registry declares, for the summary line.
func citationCount(exceptions []Exception) int {
	count := 0
	for _, e := range exceptions {
		count += len(e.Citations)
	}
	return count
}

func validateEntryCitations(e Exception, key, root string) []string {
	var findings []string
	// Declared pointers are indexed by basename+line so an abbreviated prose reference
	// (`archiver.go:124`) can be matched against a full repository path.
	declared := map[string]bool{}
	for _, c := range e.Citations {
		if c.File == "" {
			findings = append(findings, fmt.Sprintf("%s: citation with an empty file", key))
			continue
		}
		declared[citationKey(c.File, c.Line)] = true
	}
	for _, c := range e.Citations {
		findings = append(findings, validateCitation(e, key, root, c)...)
	}
	// Forward direction: every repository file:line the reason's prose names must be
	// declared. Without this, a new reference added to a reason would be trusted blindly
	// again — the exact hole TASK-328 exists to close.
	for _, ref := range reasonCitation.FindAllStringSubmatch(e.Reason, -1) {
		path := ref[1]
		if externalRef.MatchString(path) {
			continue
		}
		line, err := strconv.Atoi(ref[2])
		if err != nil {
			// The pattern only matches digits; a value that does not parse cannot be a
			// citation, so there is nothing to require.
			continue
		}
		if declared[citationKey(path, line)] {
			continue
		}
		findings = append(findings, fmt.Sprintf(
			"%s: the reason cites %s but no citation declares that line -- declare it under citations: (file/line/contains) or drop the line number",
			key, ref[0]))
	}
	return findings
}

func validateCitation(e Exception, key, root string, c Citation) []string {
	if c.File == "" {
		return nil // already reported by the caller
	}
	if c.Contains == "" {
		return []string{fmt.Sprintf(
			"%s: citation %s:%d has an empty contains -- a citation must assert the content it points at",
			key, c.File, c.Line)}
	}
	if !filepath.IsLocal(filepath.FromSlash(c.File)) {
		return []string{fmt.Sprintf(
			"%s: citation file %q must be repository-relative (no leading %q, no ..)", key, c.File, "/")}
	}
	content, err := readFile(filepath.Join(root, filepath.FromSlash(c.File)))
	if err != nil {
		return []string{fmt.Sprintf("%s: citation %s cannot be read: %v", key, c.File, err)}
	}
	lines := citationLines(content)
	if c.Line < 1 || c.Line > len(lines) {
		return []string{fmt.Sprintf(
			"%s: citation %s:%d is outside the file (%d lines)", key, c.File, c.Line, len(lines))}
	}
	got := lines[c.Line-1]
	if !strings.Contains(got, c.Contains) {
		return []string{fmt.Sprintf(
			"%s: citation %s:%d does not contain %q (the line reads %q)",
			key, c.File, c.Line, c.Contains, excerpt(got))}
	}
	// Reverse direction: the reason is the human-readable copy of the same pointer, so a
	// citation the reason does not name means one of the two was edited alone.
	if !strings.Contains(e.Reason, filepath.Base(c.File)) {
		return []string{fmt.Sprintf(
			"%s: citation %s:%d is not named by the entry reason (it does not mention %q) -- keep the reason and the citation in step",
			key, c.File, c.Line, filepath.Base(c.File))}
	}
	if !containsNumber(e.Reason, c.Line) {
		return []string{fmt.Sprintf(
			"%s: citation %s:%d is not named by the entry reason (it does not mention line %d) -- keep the reason and the citation in step",
			key, c.File, c.Line, c.Line)}
	}
	return nil
}

// citationKey identifies a pointer the way an abbreviated prose reference does: by the file's
// basename and the line, which is what `archiver.go:124` gives a reader.
func citationKey(file string, line int) string {
	return strings.ToLower(filepath.Base(filepath.FromSlash(file))) + ":" + strconv.Itoa(line)
}

// citationLines splits a file into lines, ignoring a single trailing newline so the count is
// the number of real lines rather than one more.
func citationLines(content []byte) []string {
	text := strings.TrimSuffix(string(content), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// containsNumber reports whether text mentions n as a whole number (2685 must not be satisfied
// by the digits inside 12685).
func containsNumber(text string, n int) bool {
	want := strconv.Itoa(n)
	for start := 0; start <= len(text)-len(want); {
		index := strings.Index(text[start:], want)
		if index < 0 {
			return false
		}
		begin := start + index
		end := begin + len(want)
		leftOK := begin == 0 || !isASCIIDigit(text[begin-1])
		rightOK := end == len(text) || !isASCIIDigit(text[end])
		if leftOK && rightOK {
			return true
		}
		start = begin + 1
	}
	return false
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// excerpt caps a cited line for the finding so one long line cannot flood the gate output.
func excerpt(line string) string {
	const limit = 160
	runes := []rune(line)
	if len(runes) <= limit {
		return line
	}
	return string(runes[:limit]) + "…"
}
