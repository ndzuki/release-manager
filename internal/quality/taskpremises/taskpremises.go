// Package taskpremises audits whether a planning/investigation task card still
// records when its premises were last re-checked against the repository.
//
// Why this is an audit and not a gate: both fields it reads are written by a
// human, and neither finding proves the card's premises are wrong.
//
//   - `verified_head` moving forward N commits does NOT mean the premise went
//     stale: unrelated commits advance HEAD all the time. It only means "nobody
//     has recorded a re-check since that point", which is a prompt to look, not
//     a verdict.
//   - `verified_at` aging out is likewise a heuristic. A card can sit `ready`
//     for months with a premise that is still exactly true.
//   - The audit cannot read the premise itself. It cannot tell whether the
//     thing the card assumes (a missing feature, an unmerged PR, an
//     unimplemented gate) still holds; only a human re-running the check can.
//   - A missing or unparsable field is reported as "no usable record", NOT as
//     "never checked": the two are indistinguishable from the card alone, and
//     that ambiguity is the failure this convention exists to prevent (five
//     premises were rewritten in one session because nothing said when they had
//     last been verified).
//
// So the findings go to a human, the exit status never fails on them, and the
// package is deliberately NOT part of `make quality`.
package taskpremises

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Finding kinds. The kind is stable and machine-readable; the message is for a
// human reading the audit output.
const (
	// KindUnrecorded: the card lacks a usable `verified_at` or `verified_head`.
	KindUnrecorded = "unrecorded"
	// KindStale: `verified_at` is older than the configured maximum age.
	KindStale = "stale"
	// KindHeadMoved: the recorded `verified_head` is an ancestor of the
	// reference head and N > 0 commits have landed since.
	KindHeadMoved = "head_moved"
	// KindIncomparable: `verified_head` is unknown to the repository, or it is
	// not an ancestor of the reference head, so "how far ahead" has no answer.
	// This is reported instead of guessing a commit count.
	KindIncomparable = "incomparable"
)

// dateLayout is the only accepted `verified_at` shape.
const dateLayout = "2006-01-02"

// Card is the frontmatter subset this audit reads.
type Card struct {
	Path         string
	Status       string
	VerifiedAt   string
	VerifiedHead string
}

// Ready reports whether the card claims work that has not started yet. Only
// `ready` cards are audited: `done`, `closed`, `in-progress`, `draft`,
// `blocked`, `superseded` and `withdrawn` cards have already had their premises
// resolved (or are not making a start-work claim), so a missing re-check record
// is not a finding for them.
func (c Card) Ready() bool { return c.Status == "ready" }

// Options configures a check run. Every external fact is injected so the core
// is a pure function and tests do not depend on real git or the wall clock.
type Options struct {
	// Now is the reference point for staleness. Required for the age check.
	// The comparison is day-granular, because `verified_at` is a date: Check
	// truncates Now to UTC midnight itself, so callers may pass any wall-clock
	// instant (the CLI passes time.Now().UTC()).
	Now time.Time
	// MaxAge is the largest acceptable gap between Now and `verified_at`.
	// A gap exactly equal to MaxAge is fresh: the comparison is strictly
	// greater-than, so "verified 14 days ago" with MaxAge 14 days does not
	// report. MaxAge <= 0 disables the age check.
	MaxAge time.Duration
	// Head is the reference head sha. The `make audit-task-premises` target
	// passes **main**'s sha (origin/main, else main), not the current checkout
	// HEAD, so commits on a feature branch do not inflate N. Empty means the
	// caller did not supply one, so the head comparison is skipped entirely
	// (never reported as incomparable: "not asked" is not "cannot compare").
	Head string
	// CommitsAhead answers how many commits separate `verified_head` from the
	// reference head. ok=false means the sha is unknown or is not an ancestor
	// of it, so no count can be trusted. Nil skips the comparison.
	CommitsAhead func(sha string) (int, bool)
}

// Finding is one card whose re-check record is missing, old, or behind HEAD.
type Finding struct {
	Path    string
	Kind    string
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: [%s] %s", f.Path, f.Kind, f.Message)
}

// Report is the outcome of an audit run.
type Report struct {
	// Cards is every card handed to Check.
	Cards int
	// Checked is the number of `ready` cards examined.
	Checked int
	// Skipped is the number of cards ignored because their status is not
	// `ready`.
	Skipped int
	// Findings are ordered by path then kind so a run is reproducible.
	Findings []Finding
}

// Count returns how many findings carry the given kind.
func (r *Report) Count(kind string) int {
	n := 0
	for _, finding := range r.Findings {
		if finding.Kind == kind {
			n++
		}
	}
	return n
}

// Check audits every card. Cards that are not `ready` are counted in Skipped
// and produce no findings.
func Check(cards []Card, opts Options) *Report {
	// `verified_at` is a date parsed at UTC midnight, so truncate Now the same
	// way before comparing: a card verified exactly MaxAge days ago is fresh
	// no matter what time of day the audit runs. Truncate counts from the epoch
	// (midnight UTC), so on a UTC time it lands on the current UTC midnight.
	opts.Now = opts.Now.UTC().Truncate(24 * time.Hour)

	report := &Report{Cards: len(cards)}
	for _, card := range cards {
		if !card.Ready() {
			report.Skipped++
			continue
		}
		report.Checked++
		report.Findings = append(report.Findings, checkCard(card, opts)...)
	}
	sortFindings(report.Findings)
	return report
}

// checkCard returns every finding that applies to one ready card. A card can
// produce more than one (for example, both stale and head-moved); they are
// different facts and collapsing them would hide one.
func checkCard(card Card, opts Options) []Finding {
	at, atOK := parseDate(card.VerifiedAt)
	head := strings.TrimSpace(card.VerifiedHead)
	if !atOK || head == "" {
		return []Finding{{
			Path:    card.Path,
			Kind:    KindUnrecorded,
			Message: unrecordedMessage(card.VerifiedAt, atOK, head),
		}}
	}

	var findings []Finding
	if opts.MaxAge > 0 {
		if age := opts.Now.Sub(at); age > opts.MaxAge {
			findings = append(findings, Finding{
				Path: card.Path,
				Kind: KindStale,
				Message: fmt.Sprintf("last verified %s (%s ago, limit %s); re-run the premise check and update verified_at",
					at.Format(dateLayout), humanDays(age), humanDays(opts.MaxAge)),
			})
		}
	}

	if opts.Head != "" && opts.CommitsAhead != nil {
		switch n, ok := opts.CommitsAhead(head); {
		case !ok:
			findings = append(findings, Finding{
				Path: card.Path,
				Kind: KindIncomparable,
				Message: fmt.Sprintf("verified_head %s is not an ancestor of the reference head %s (or is unknown); cannot count the commits since",
					head, opts.Head),
			})
		case n > 0:
			findings = append(findings, Finding{
				Path: card.Path,
				Kind: KindHeadMoved,
				Message: fmt.Sprintf("HEAD has moved %d commit(s) since verified_head %s; confirm the premise still holds",
					n, head),
			})
		}
	}
	return findings
}

// unrecordedMessage names every part of the record that is missing or unusable.
func unrecordedMessage(verifiedAt string, atOK bool, head string) string {
	var problems []string
	switch {
	case strings.TrimSpace(verifiedAt) == "":
		problems = append(problems, "verified_at is missing")
	case !atOK:
		problems = append(problems, fmt.Sprintf("verified_at %q is not YYYY-MM-DD", verifiedAt))
	}
	if head == "" {
		problems = append(problems, "verified_head is missing")
	}
	return "no usable premise re-check record (" + strings.Join(problems, "; ") +
		"); add verified_at + verified_head before starting"
}

// parseDate accepts exactly the YYYY-MM-DD layout.
func parseDate(raw string) (time.Time, bool) {
	t, err := time.Parse(dateLayout, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// humanDays renders a duration in whole days, rounding down, so the message
// stays readable for both the age and the limit.
func humanDays(d time.Duration) string {
	days := int(d / (24 * time.Hour))
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}

func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Kind < findings[j].Kind
	})
}

// ParseCard reads the frontmatter subset the audit needs. The frontmatter is
// the leading YAML block delimited by "---" lines; values are unquoted so a
// card may write either `status: ready` or `status: "ready"`. Cards without a
// frontmatter block are an error: silently skipping them would hide the very
// cards the audit is meant to look at.
func ParseCard(path string) (Card, error) {
	f, err := os.Open(path) //nolint:gosec // trusted local checkout
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
		case "verified_at":
			card.VerifiedAt = value
		case "verified_head":
			card.VerifiedHead = value
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
