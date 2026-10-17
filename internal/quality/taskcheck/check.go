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
// closed must declare merge_status merged and carry a non-empty pr_url, and EVERY
// pull request it cites must be provably merged (a delivery can land in more than
// one, TASK-227). Two independent evidence sources are accepted:
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
//
// Not every delivery can be a pull request, though: the dotfiles/plugins this
// machine manages with chezmoi live in a different repository, whose watcher
// commits and pushes straight to main (TASK-314/319/322). Sending those cards
// down the PR path would mean either a fabricated PR URL or a permanent
// "ready" card, so they get a third value instead -- merge_status: direct, with
//
//	pr_url: direct:<40-hex-sha>@<repo-path>
//	evidence_paths: <repo-relative path>, <repo-relative path>, ...
//
// The direct channel is verified against the named repository's OWN object
// database: the commit must be reachable from a ref there, and EVERY declared
// evidence_paths entry must be among the files that commit changed. That is
// strictly more than "the sha exists": a sha that resolves but does not touch
// the delivered paths is a contradiction, not evidence (the negation of the
// scratch-work failure the 2026-09 audit found). The existing PR rules are not
// relaxed by its presence -- merge_status selects exactly one channel, and the
// pointer's scheme must match it.
package taskcheck

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
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
	// KindEvidenceChannelMismatch: pr_url claims the direct-commit scheme while
	// merge_status does not declare merge_status=direct.
	KindEvidenceChannelMismatch = "evidence_channel_mismatch"
	// KindDirectEvidenceInvalid: merge_status=direct, but the direct evidence
	// itself is malformed (bad sha or repo, or no usable evidence_paths).
	KindDirectEvidenceInvalid = "direct_evidence_invalid"
	// KindDirectPathsMismatch: the cited commit is reachable, but it does not
	// change every path the card declares in evidence_paths.
	KindDirectPathsMismatch = "direct_paths_mismatch"
	// KindDirectEvidenceUnverified: the cited commit is not reachable in the
	// local object database of the repository the card names.
	KindDirectEvidenceUnverified = "direct_evidence_unverified"
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
	// EvidencePaths is the direct channel's declaration: the repository-relative
	// paths this card claims to have delivered, matched against the cited
	// commit's changed files. It is only read when merge_status is direct.
	EvidencePaths []string
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
	// directCommits/directFiles hold what the collector found in each repository
	// a `direct:` ref names, keyed by the repo string exactly as the card wrote
	// it (so the card's own value is the lookup key -- never a local absolute
	// path, which would not survive to another machine).
	directCommits map[DirectRef]struct{}
	directFiles   map[DirectRef]map[string]struct{}
}

// NewEvidence returns an empty evidence set.
func NewEvidence() *Evidence {
	return &Evidence{
		commits:       make(map[string]struct{}),
		prFromSubject: make(map[int]string),
		prs:           make(map[int]PREvidence),
		directCommits: make(map[DirectRef]struct{}),
		directFiles:   make(map[DirectRef]map[string]struct{}),
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

// DirectRef is a non-PR delivery pointer: a commit in a repository other than
// this one, written as `direct:<40-hex-sha>@<repo-path>`. The repo is a path
// (`/abs/...` or `~/...`) because the ledger gate is a local, offline gate --
// resolving a remote repository identity would need the network, and the
// existing PR channel already degrades to UNVERIFIED when it is offline.
type DirectRef struct {
	Repo string
	SHA  string
}

// String renders the ref the way a card writes it.
func (r DirectRef) String() string {
	return "direct:" + r.SHA + "@" + r.Repo
}

var directRefRe = regexp.MustCompile(`^direct:([0-9a-fA-F]{40})@(\S+)$`)

// ParseDirectRef parses the direct-commit pointer. It is strict on purpose: a
// malformed pointer must be reported, never quietly ignored (an unparsed value
// that still "looked like evidence" is how a card would smuggle a bogus sha past
// the gate).
func ParseDirectRef(raw string) (DirectRef, bool) {
	m := directRefRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return DirectRef{}, false
	}
	repo := m[2]
	if !strings.HasPrefix(repo, "/") && !strings.HasPrefix(repo, "~/") {
		return DirectRef{}, false
	}
	return DirectRef{Repo: repo, SHA: strings.ToLower(m[1])}, true
}

// isDirectRef reports whether a pr_url claims the direct-commit scheme at all,
// including a malformed one. The distinction matters: a malformed `direct:`
// pointer must be diagnosed as such instead of being handed to the pull-request
// path, which would report the failure for the wrong reason.
func isDirectRef(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), "direct:")
}

// AddDirectCommit records that a `direct:` ref resolved to a commit reachable
// from a ref in that repository.
func (e *Evidence) AddDirectCommit(repo, sha string) {
	if repo == "" || sha == "" {
		return
	}
	e.directCommits[DirectRef{Repo: repo, SHA: strings.ToLower(sha)}] = struct{}{}
}

// AddDirectFile records one path the cited commit changed.
func (e *Evidence) AddDirectFile(repo, sha, file string) {
	if repo == "" || sha == "" || file == "" {
		return
	}
	ref := DirectRef{Repo: repo, SHA: strings.ToLower(sha)}
	if e.directFiles[ref] == nil {
		e.directFiles[ref] = make(map[string]struct{})
	}
	e.directFiles[ref][file] = struct{}{}
}

// HasDirectCommit reports whether the ref's commit was found reachable in the
// named repository's local object database.
func (e *Evidence) HasDirectCommit(ref DirectRef) bool {
	_, ok := e.directCommits[ref]
	return ok
}

// DirectFiles returns the set of paths the ref's commit changed. A verified ref
// always has this populated by the collector; an empty set means the commit
// changed nothing, which cannot cover a non-empty evidence_paths declaration.
func (e *Evidence) DirectFiles(ref DirectRef) map[string]struct{} {
	return e.directFiles[ref]
}

// ParseDirectEvidence reads the collector's output (scripts/collect-direct-evidence.sh):
// tab-separated records, one per line.
//
//	commit\t<repo>\t<full-sha>
//	file\t<repo>\t<full-sha>\t<path>
//
// A malformed line is an error rather than a skipped record: the file is
// generated by this project's own collector, so a line it cannot parse means the
// collector is broken, and silently dropping it would weaken the gate.
func (e *Evidence) ParseDirectEvidence(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		switch {
		case len(fields) == 3 && fields[0] == "commit":
			e.AddDirectCommit(fields[1], fields[2])
		case len(fields) == 4 && fields[0] == "file":
			e.AddDirectFile(fields[1], fields[2], fields[3])
		default:
			return fmt.Errorf("malformed direct evidence line %q", line)
		}
	}
	return scanner.Err()
}

// DirectRefs lists the unique direct-commit refs a card set cites, in
// first-seen order. Malformed values are skipped here on purpose: the collector
// cannot gather evidence for them, and Check reports them as invalid -- this
// function only says which repositories to look in.
func DirectRefs(cards []Card) []DirectRef {
	seen := make(map[DirectRef]struct{})
	refs := make([]DirectRef, 0, len(cards))
	for _, card := range cards {
		ref, ok := ParseDirectRef(card.PRURL)
		if !ok {
			continue
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
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
	// Checked counts the task cards this run looked at.
	Checked int
	// RequirementsChecked counts the requirement records when the caller asked for the REQ
	// side of the ledger as well. It is a separate counter on purpose: the two are different
	// things, and adding them together made the summary claim a card count that no longer
	// described the card set (review of TASK-227 surfaced the 265-vs-203 mismatch).
	RequirementsChecked int
	Verified            int
	Findings            []Finding
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
		direct, claimsDirect := ParseDirectRef(card.PRURL)
		findings = append(findings, channelFindings(card, claimsDirect)...)
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

		// The direct channel. Entered whenever either the pointer or the declared
		// merge_status is direct, so a mismatch can never fall through to the PR
		// channel and collect a second, misleading finding.
		if isDirectRef(card.PRURL) || card.MergeStatus == "direct" {
			if card.MergeStatus == "direct" && claimsDirect {
				findings = append(findings, verifyDirectPaths(card, direct, ev)...)
			}
			if len(findings) == 0 && claimsDirect {
				result.Verified++
			}
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

// channelFindings reports the problems in the (merge_status, pr_url) pair itself,
// before any evidence is consulted: the two fields select the SAME evidence
// channel, so a card that declares one and cites the other is contradicting
// itself. Letting such a card fall through would have the PR path report it as
// merely unverified, which understates the problem.
func channelFindings(card Card, claimsDirect bool) []Finding {
	switch card.MergeStatus {
	case "merged":
		if isDirectRef(card.PRURL) {
			return []Finding{{
				Path:     card.Path,
				Kind:     KindEvidenceChannelMismatch,
				Severity: SeverityViolation,
				Message: fmt.Sprintf("merge_status=merged cites a direct-commit pr_url %q; a direct delivery requires merge_status=direct",
					card.PRURL),
			}}
		}
	case "direct":
		// An empty pr_url is reported by KindMissingPRURL, exactly as it is for a
		// merged card; saying it twice would imply two problems.
		if !claimsDirect && card.PRURL != "" {
			return []Finding{{
				Path:     card.Path,
				Kind:     KindDirectEvidenceInvalid,
				Severity: SeverityViolation,
				Message: fmt.Sprintf("merge_status=direct requires pr_url of the form direct:<40-hex-sha>@<repo-path>, got %q",
					card.PRURL),
			}}
		}
	default:
		return []Finding{{
			Path:     card.Path,
			Kind:     KindMergeStatusMismatch,
			Severity: SeverityViolation,
			Message: fmt.Sprintf("status=%s requires merge_status=merged or merge_status=direct, got %q",
				card.Status, card.MergeStatus),
		}}
	}
	return nil
}

// verifyPR checks that every pull request the card names is merged with a merge commit
// present in the local history.//
// A delivery can genuinely land in more than one pull request (TASK-227: the server half and
// the console half of one task shipped separately), so pr_url may list several URLs separated
// by commas, semicolons or whitespace. The rule is not relaxed by that: EACH PR needs its own
// evidence, so listing more PRs can only ever make a card harder to verify, never easier. The
// card's PR URL is the primary one, and it should be listed first.
func verifyPR(card Card, ev *Evidence) (bool, *Finding) {
	numbers, ok := githubPRNumbers(card.PRURL)
	if !ok {
		return false, &Finding{
			Path:     card.Path,
			Kind:     KindPRUnverified,
			Severity: SeverityUnverified,
			Message:  fmt.Sprintf("pr_url %q is not a GitHub pull request URL (or lists one that is not); cannot verify the merge", card.PRURL),
		}
	}
	for _, n := range numbers {
		if finding := verifyOnePR(card, ev, n); finding != nil {
			return false, finding
		}
	}
	return true, nil
}

// verifyOnePR checks one PR number and returns the finding that stops the card, if any.
func verifyOnePR(card Card, ev *Evidence, n int) *Finding {
	ref := fmt.Sprintf("pull/%d", n)
	if pr, known := ev.prs[n]; known {
		if !strings.EqualFold(pr.State, "MERGED") {
			return &Finding{
				Path:     card.Path,
				Kind:     KindPRNotMerged,
				Severity: SeverityViolation,
				Message:  fmt.Sprintf("%s (%s) is %s, not MERGED", ref, card.PRURL, strings.ToUpper(pr.State)),
			}
		}
		oid := ""
		if pr.MergeCommit != nil {
			oid = pr.MergeCommit.OID
		}
		if ev.HasCommit(oid) {
			return nil
		}
		if local, found := ev.prFromSubject[n]; found {
			return &Finding{
				Path:     card.Path,
				Kind:     KindPRUnverified,
				Severity: SeverityUnverified,
				Message: fmt.Sprintf("%s is MERGED but its merge commit %s is not in the local history; matched local merge commit %s instead",
					ref, oid, local),
			}
		}
		return &Finding{
			Path:     card.Path,
			Kind:     KindPRUnverified,
			Severity: SeverityUnverified,
			Message: fmt.Sprintf("%s is MERGED but merge commit %s is not reachable in the local history",
				ref, oid),
		}
	}

	if _, found := ev.prFromSubject[n]; found {
		return nil
	}
	return &Finding{
		Path:     card.Path,
		Kind:     KindPRUnverified,
		Severity: SeverityUnverified,
		Message:  fmt.Sprintf("%s has no merge evidence in the local git history and no gh PR data", ref),
	}
}

// verifyDirectPaths validates a direct (non-PR) delivery's own declarations and
// then checks them against the named repository's object database: the commit
// must be reachable there, and every path the card declares in evidence_paths
// must be among the files that commit changed.
//
// The comparison is a superset check on purpose. The automated channel these
// deliveries ride on (the chezmoi watcher) commits in batches -- "auto(dotfiles):
// sync N file(s)" -- so one delivery can share its commit with unrelated files; a
// batch can only ever mean the commit touched MORE than this card. Requiring set
// equality would make an honest card fail for someone else's file, while
// requiring every declared path to be present still refuses an unrelated commit.
func verifyDirectPaths(card Card, ref DirectRef, ev *Evidence) []Finding {
	clean, invalid := normalizeEvidencePaths(card.EvidencePaths)
	switch {
	case len(clean) == 0:
		return []Finding{{
			Path:     card.Path,
			Kind:     KindDirectEvidenceInvalid,
			Severity: SeverityViolation,
			Message:  "merge_status=direct requires a non-empty evidence_paths list of repository-relative paths",
		}}
	case len(invalid) > 0:
		return []Finding{{
			Path:     card.Path,
			Kind:     KindDirectEvidenceInvalid,
			Severity: SeverityViolation,
			Message: fmt.Sprintf("evidence_paths entries must be repository-relative paths (no absolute, ~ or ../ entries), got %s",
				strings.Join(invalid, ", ")),
		}}
	}
	if !ev.HasDirectCommit(ref) {
		return []Finding{{
			Path:     card.Path,
			Kind:     KindDirectEvidenceUnverified,
			Severity: SeverityUnverified,
			Message: fmt.Sprintf("%s is not reachable in the local git history of %s; cannot verify the direct delivery",
				ref, ref.Repo),
		}}
	}
	changed := ev.DirectFiles(ref)
	var missing []string
	for _, p := range clean {
		if _, ok := changed[p]; !ok {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return []Finding{{
			Path:     card.Path,
			Kind:     KindDirectPathsMismatch,
			Severity: SeverityViolation,
			Message: fmt.Sprintf("%s changed %d file(s) in %s, not including the declared evidence_paths: %s",
				ref, len(changed), ref.Repo, strings.Join(missing, ", ")),
		}}
	}
	return nil
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

// githubPRNumbers extracts every pull request number a card names. Segments are separated by
// commas, semicolons or whitespace. A segment that is not a GitHub pull request URL makes the
// whole value invalid instead of being skipped: silently ignoring one would let an
// unverifiable PR hide behind a verifiable one.
func githubPRNumbers(raw string) ([]int, bool) {
	segments := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
	if len(segments) == 0 {
		return nil, false
	}
	numbers := make([]int, 0, len(segments))
	for _, segment := range segments {
		n, ok := githubPRNumber(segment)
		if !ok {
			return nil, false
		}
		numbers = append(numbers, n)
	}
	return numbers, true
}

// normalizeEvidencePaths separates usable declarations from unusable ones. An
// unusable entry is not dropped silently: dropping it would let one checked path
// cover for an unchecked one, the same way a bad segment invalidates a whole
// multi-PR pr_url.
func normalizeEvidencePaths(paths []string) (clean, invalid []string) {
	for _, p := range paths {
		if c, ok := cleanEvidencePath(p); ok {
			clean = append(clean, c)
		} else {
			invalid = append(invalid, p)
		}
	}
	return clean, invalid
}

// cleanEvidencePath reports whether a declared delivery path can be matched
// against `git show --name-only` output. Absolute paths, home paths and parent
// escapes are rejected: they can never match a repository-relative path, so
// accepting them would let a card declare something that is never checked.
func cleanEvidencePath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return "", false
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// splitEvidencePaths splits the comma-separated evidence_paths value. Empty
// segments are ignored, matching the multi-PR pr_url convention.
func splitEvidencePaths(value string) []string {
	var paths []string
	for _, segment := range strings.Split(value, ",") {
		if segment = strings.TrimSpace(segment); segment != "" {
			paths = append(paths, segment)
		}
	}
	return paths
}

// ParseCard reads the frontmatter subset the gate needs. The frontmatter is the
// leading YAML block delimited by "---" lines; values are unquoted so a card may
// write either `status: done` or `status: "done"`.
func ParseCard(cardPath string) (Card, error) {
	f, err := os.Open(cardPath)
	if err != nil {
		return Card{}, fmt.Errorf("open %s: %w", cardPath, err)
	}
	defer f.Close()

	card := Card{Path: cardPath}
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
		case "evidence_paths":
			card.EvidencePaths = splitEvidencePaths(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return Card{}, fmt.Errorf("read %s: %w", cardPath, err)
	}
	if !inFrontmatter || !closed {
		return Card{}, fmt.Errorf("%s: no frontmatter block found", cardPath)
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
