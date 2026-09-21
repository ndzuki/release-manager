// Package taskcheck validates that the delivery ledger (the vault's Tasks/*.md
// cards) agrees with the merge facts in git.
//
// Why this gate exists: PROJECT-CONVENTIONS.md promises that "卡片状态与 merge
// 事实必须一致，并应由门禁校验", but no gate did. The 2026-09 audit found a
// card marked closed while its merge_status said unmerged, and several cards
// whose PR URL pointed at no commit in the repository history -- a ledger that
// drifts silently is how a "done" claim ships without a merged change.
//
// The rule is deliberately narrow and mechanical: a card whose status is done or
// closed must declare merge_status merged and carry a non-empty pr_url, and that
// PR must be provably merged. Two independent evidence sources are accepted:
//
//   - gh pr list output (number/state/mergeCommit), whose mergeCommit OID must
//     also exist in the local git history; and
//   - the local `git log` subjects, matched on the "Merge pull request #N from"
//     shape GitHub writes for merge commits.
//
// A PR with neither is reported UNVERIFIED rather than silently accepted. It
// fails the gate unless the caller sets AllowUnverified (the explicit
// ALLOW_UNVERIFIED_TASKS=1 escape hatch), so an offline checkout cannot pass by
// having no evidence at all.
package taskcheck

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Finding kinds. The kind is stable and machine-readable; the message is for a
// human reading the failing run.
const (
	// KindMergeStatusMismatch: status is done/closed but merge_status is not merged.
	KindMergeStatusMismatch = "merge_status_mismatch"
	// KindMissingPRURL: status is done/closed but pr_url is empty.
	KindMissingPRURL = "missing_pr_url"
	// KindPRNotMerged: the PR exists but its state is not MERGED.
	KindPRNotMerged = "pr_not_merged"
	// KindPRUnverified: no evidence the PR was merged (offline or unknown PR).
	KindPRUnverified = "pr_unverified"
)

// Severity separates ledger contradictions (always fatal) from missing evidence
// (fatal unless the caller explicitly allows unverified cards).
type Severity int

const (
	// SeverityViolation is a contradiction between the card and the evidence.
	SeverityViolation Severity = iota
	// SeverityUnverified means the card may be fine but no evidence proves it.
	SeverityUnverified
)

func (s Severity) String() string {
	if s == SeverityUnverified {
		return "UNVERIFIED"
	}
	return "VIOLATION"
}

// Card is the frontmatter subset this gate reads.
type Card struct {
	Path          string
	Status        string
	MergeStatus   string
	PRURL         string
	ClosureReason string
}

// NonPRClosureReasons are documented closures that do not imply a merge, so the
// merge evidence rule cannot apply to them: a card closed because the work was
// already implemented, superseded, or landed before this project used pull
// requests has no PR to point at. The reason itself is the evidence, and it
// must be present -- this is an alternative record, not a bypass.
var NonPRClosureReasons = map[string]bool{
	"already-implemented": true,
	"already_implemented": true,
	"superseded":          true,
	"pre-pr-workflow":     true,
}

// Merged reports whether the card claims a completed delivery.
func (c Card) Merged() bool {
	return c.Status == "done" || c.Status == "closed"
}

// MergeCommitRef is gh's merge commit reference for a merged PR.
type MergeCommitRef struct {
	OID string `json:"oid"`
}

// PREvidence is one entry of the `gh pr list --json number,state,mergeCommit`
// output.
type PREvidence struct {
	Number      int             `json:"number"`
	State       string          `json:"state"`
	MergeCommit *MergeCommitRef `json:"mergeCommit"`
}

// Evidence is the merge evidence collected by the caller (the Makefile runs git
// and gh; this package stays free of os/exec so the SDK-only gate keeps passing).
type Evidence struct {
	commits map[string]struct{}
	// prFromSubject maps a PR number to the merge commit that names it, built
	// from the "Merge pull request #N from ..." subject shape.
	prFromSubject map[int]string
	prs           map[int]PREvidence
}

// NewEvidence returns an empty evidence set.
func NewEvidence() *Evidence {
	return &Evidence{
		commits:       make(map[string]struct{}),
		prFromSubject: make(map[int]string),
		prs:           make(map[int]PREvidence),
	}
}

var mergePullSubject = regexp.MustCompile(`^Merge pull request #(\d+) from `)

// AddCommit records a reachable commit. The subject is inspected for the
// GitHub merge-commit shape so a local `git log` alone can verify a PR.
//
// Only the merge-commit subject is trusted: the "(#N)" squash shape is
// ambiguous in this repository, where it also encodes task numbers ("(#017)",
// "(#004)"), so treating it as a PR number would fabricate evidence.
func (e *Evidence) AddCommit(oid, subject string) {
	if oid == "" {
		return
	}
	e.commits[oid] = struct{}{}
	if m := mergePullSubject.FindStringSubmatch(subject); m != nil {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			if _, seen := e.prFromSubject[n]; !seen {
				e.prFromSubject[n] = oid
			}
		}
	}
}

// AddPR records gh's view of a pull request.
func (e *Evidence) AddPR(pr PREvidence) {
	if pr.Number <= 0 {
		return
	}
	e.prs[pr.Number] = pr
}

// HasCommit reports whether the OID is reachable in the local history.
func (e *Evidence) HasCommit(oid string) bool {
	if oid == "" {
		return false
	}
	_, ok := e.commits[oid]
	return ok
}

// ParseGitLog reads `git log --all --format=%H%x09%s` output.
func (e *Evidence) ParseGitLog(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		oid, subject, ok := strings.Cut(line, "\t")
		if !ok {
			// A subject may contain tabs; a line without one is not ours.
			continue
		}
		e.AddCommit(strings.TrimSpace(oid), subject)
	}
	return scanner.Err()
}

// ParseGHPullList reads the JSON array produced by
// `gh pr list --state all --limit <n> --json number,state,mergeCommit`. An empty
// input means gh produced nothing (offline, unauthenticated, or the command was
// killed) and is treated as "no gh evidence", not as a malformed document.
func (e *Evidence) ParseGHPullList(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read gh pr list output: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	var prs []PREvidence
	if err := json.Unmarshal(data, &prs); err != nil {
		return fmt.Errorf("decode gh pr list output: %w", err)
	}
	for _, pr := range prs {
		e.AddPR(pr)
	}
	return nil
}

// Options configures a check run.
type Options struct {
	// AllowUnverified downgrades UNVERIFIED findings from fatal to advisory.
	AllowUnverified bool
}

// Finding is one reported problem.
type Finding struct {
	Path     string
	Kind     string
	Severity Severity
	Message  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: [%s] %s: %s", f.Path, f.Kind, f.Severity, f.Message)
}

// Result is the outcome of a check run.
type Result struct {
	Checked  int
	Verified int
	Findings []Finding
}

// Failed reports whether the run should exit non-zero.
func (r *Result) Failed(opts Options) bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityViolation || !opts.AllowUnverified {
			return true
		}
	}
	return false
}

// Check validates every completed card against the evidence.
func Check(cards []Card, ev *Evidence) *Result {
	result := &Result{}
	for _, card := range cards {
		if !card.Merged() {
			continue
		}
		result.Checked++

		// A documented non-PR closure is the alternative record: the work was
		// already implemented, superseded, or landed before the project used
		// pull requests, so there is no PR to verify. The gate still requires
		// the reason to be stated.
		if NonPRClosureReasons[card.ClosureReason] {
			result.Verified++
			continue
		}

		var findings []Finding
		if card.MergeStatus != "merged" {
			findings = append(findings, Finding{
				Path:     card.Path,
				Kind:     KindMergeStatusMismatch,
				Severity: SeverityViolation,
				Message: fmt.Sprintf("status=%s requires merge_status=merged, got %q",
					card.Status, card.MergeStatus),
			})
		}
		if card.PRURL == "" {
			findings = append(findings, Finding{
				Path:     card.Path,
				Kind:     KindMissingPRURL,
				Severity: SeverityViolation,
				Message:  fmt.Sprintf("status=%s requires a non-empty pr_url", card.Status),
			})
			result.Findings = append(result.Findings, findings...)
			continue
		}

		verified, finding := verifyPR(card, ev)
		if finding != nil {
			findings = append(findings, *finding)
		}
		if len(findings) == 0 && verified {
			result.Verified++
		}
		result.Findings = append(result.Findings, findings...)
	}
	return result
}

// verifyPR checks that the card's pr_url is a merged PR whose merge commit is
// present in the local history.
func verifyPR(card Card, ev *Evidence) (bool, *Finding) {
	n, ok := githubPRNumber(card.PRURL)
	if !ok {
		return false, &Finding{
			Path:     card.Path,
			Kind:     KindPRUnverified,
			Severity: SeverityUnverified,
			Message:  fmt.Sprintf("pr_url %q is not a GitHub pull request URL; cannot verify the merge", card.PRURL),
		}
	}

	if pr, known := ev.prs[n]; known {
		if !strings.EqualFold(pr.State, "MERGED") {
			return false, &Finding{
				Path:     card.Path,
				Kind:     KindPRNotMerged,
				Severity: SeverityViolation,
				Message:  fmt.Sprintf("pr_url %q is %s, not MERGED", card.PRURL, strings.ToUpper(pr.State)),
			}
		}
		oid := ""
		if pr.MergeCommit != nil {
			oid = pr.MergeCommit.OID
		}
		if ev.HasCommit(oid) {
			return true, nil
		}
		if local, found := ev.prFromSubject[n]; found {
			return true, &Finding{
				Path:     card.Path,
				Kind:     KindPRUnverified,
				Severity: SeverityUnverified,
				Message: fmt.Sprintf("pr_url %q is MERGED but its merge commit %s is not in the local history; matched local merge commit %s instead",
					card.PRURL, oid, local),
			}
		}
		return false, &Finding{
			Path:     card.Path,
			Kind:     KindPRUnverified,
			Severity: SeverityUnverified,
			Message: fmt.Sprintf("pr_url %q is MERGED but merge commit %s is not reachable in the local history",
				card.PRURL, oid),
		}
	}

	if _, found := ev.prFromSubject[n]; found {
		return true, nil
	}
	return false, &Finding{
		Path:     card.Path,
		Kind:     KindPRUnverified,
		Severity: SeverityUnverified,
		Message:  fmt.Sprintf("pr_url %q has no merge evidence in the local git history and no gh PR data", card.PRURL),
	}
}

var githubPRRe = regexp.MustCompile(`^https?://github\.com/[^/]+/[^/]+/pull/(\d+)(?:[/?#].*)?$`)

func githubPRNumber(url string) (int, bool) {
	m := githubPRRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// ParseCard reads the frontmatter subset the gate needs. The frontmatter is the
// leading YAML block delimited by "---" lines; values are unquoted so a card may
// write either `status: done` or `status: "done"`.
func ParseCard(path string) (Card, error) {
	f, err := os.Open(path)
	if err != nil {
		return Card{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	card := Card{Path: path}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	inFrontmatter := false
	closed := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			closed = true
			break
		}
		if !inFrontmatter {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = unquote(strings.TrimSpace(value))
		switch strings.TrimSpace(key) {
		case "status":
			card.Status = value
		case "merge_status":
			card.MergeStatus = value
		case "pr_url":
			card.PRURL = value
		case "closure_reason":
			card.ClosureReason = value
		}
	}
	if err := scanner.Err(); err != nil {
		return Card{}, fmt.Errorf("read %s: %w", path, err)
	}
	if !inFrontmatter || !closed {
		return Card{}, fmt.Errorf("%s: no frontmatter block found", path)
	}
	return card, nil
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// SortFindings orders findings by path then kind so a failing run is stable.
func SortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Kind < findings[j].Kind
	})
}
