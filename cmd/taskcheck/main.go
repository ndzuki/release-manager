// Command taskcheck validates the delivery ledger (vault Tasks/*.md) against the
// merge facts in git.
//
// Usage:
//
//	taskcheck -git-log <file> -repo-url <origin-url> [-gh-prs <file>] [-direct-evidence <file>] [-allow-unverified] <TASK-*.md>...
//	taskcheck -print-direct-refs <TASK-*.md>...
//
// The Makefile collects the evidence because this tool must not import
// os/exec (the SDK-only gate scans cmd/ and internal/): `git log --all
// --format=%H%x09%s` feeds -git-log, `git remote get-url origin` feeds
// -repo-url (the repository a card's pr_url has to name), `gh pr list
// --state all --limit <n> --json number,url,state,mergeCommit` feeds -gh-prs
// when gh is available, and scripts/collect-direct-evidence.sh feeds
// -direct-evidence for the non-PR (merge_status: direct) cards.
// -print-direct-refs prints the repositories those cards name so that script
// knows where to look; it exists so the card format is parsed in exactly one
// place.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ndzuki/release-manager/internal/quality/taskcheck"
)

func main() {
	var (
		gitLog          string
		ghPRs           string
		repoURL         string
		directEvidence  string
		printDirectRefs bool
		requirements    string
		allowUnverified bool
	)
	flag.StringVar(&gitLog, "git-log", "", "path to `git log --all --format=%H%x09%s` output")
	flag.StringVar(&ghPRs, "gh-prs", "", "path to `gh pr list --json number,url,state,mergeCommit` output (optional)")
	flag.StringVar(&repoURL, "repo-url", "", "this repository's origin URL (`git remote get-url origin`); a card's pr_url must name the same owner/repo")
	flag.StringVar(&directEvidence, "direct-evidence", "", "path to scripts/collect-direct-evidence.sh output (optional)")
	flag.BoolVar(&printDirectRefs, "print-direct-refs", false, "print the `direct:` repository/sha pairs the cards cite, one per line, and exit")
	flag.StringVar(&requirements, "requirements", "", "vault Requirements directory or REQ-*.md file list (optional: enables the REQ delivery-ledger check)")
	flag.BoolVar(&allowUnverified, "allow-unverified", false, "report UNVERIFIED cards without failing (ALLOW_UNVERIFIED_TASKS=1)")
	flag.Parse()

	paths := flag.Args()
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: taskcheck -git-log <file> -repo-url <origin-url> [-gh-prs <file>] [-direct-evidence <file>] [-allow-unverified] <TASK-*.md>...")
		os.Exit(2)
	}

	if printDirectRefs {
		printDirectRefList(paths)
		return
	}

	evidence, err := loadEvidence(gitLog, ghPRs, directEvidence)
	if err != nil {
		fmt.Fprintf(os.Stderr, "taskcheck: %v\n", err)
		os.Exit(2)
	}
	// The repository identity is not optional: without it a PR URL cannot be
	// attributed to a repository, and reporting its cards unverified is the only
	// honest answer (never a silent pass). Say so once, loudly, so the cause of a
	// whole ledger turning unverified is visible in the run output.
	if slug, ok := taskcheck.ParseRepoSlug(repoURL); ok {
		evidence.SetRepoSlug(slug)
	} else {
		fmt.Fprintf(os.Stderr, "taskcheck: warning: cannot derive this repository's owner/repo from %q; pull request evidence will be reported unverified\n", repoURL)
	}
	cards, err := loadCards(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "taskcheck: %v\n", err)
		os.Exit(2)
	}

	opts := taskcheck.Options{AllowUnverified: allowUnverified}
	result := taskcheck.Check(cards, evidence)

	// REQ side of the same ledger: a 交付记录 must mean delivered + verified,
	// and the two fields must never disagree.
	if requirements != "" {
		reqs, err := loadRequirements(requirements)
		if err != nil {
			fmt.Fprintf(os.Stderr, "taskcheck: %v\n", err)
			os.Exit(2)
		}
		mergeResults(result, taskcheck.CheckRequirements(reqs))
	}
	taskcheck.SortFindings(result.Findings)

	for _, finding := range result.Findings {
		fmt.Println(finding.String())
	}

	unverified := 0
	violations := 0
	for _, finding := range result.Findings {
		if finding.Severity == taskcheck.SeverityViolation {
			violations++
		} else {
			unverified++
		}
	}

	fmt.Println(summaryLine(*result, violations, unverified))
	if result.Failed(opts) {
		if violations == 0 && unverified > 0 {
			fmt.Fprintln(os.Stderr, "taskcheck: set ALLOW_UNVERIFIED_TASKS=1 to accept unverified merge evidence explicitly")
		}
		os.Exit(1)
	}
}

// mergeResults folds the requirement side of the ledger into the card result. It exists as a
// seam because the defect this fixes was the accumulation itself: the two populations must
// stay in separate fields, and a reviewer showed that reverting the addition kept every test
// green while the summary silently went back to reporting requirements as cards.
func mergeResults(cards, reqs *taskcheck.Result) {
	if reqs == nil {
		return
	}
	cards.Findings = append(cards.Findings, reqs.Findings...)
	cards.RequirementsChecked += reqs.Checked
}

// summaryLine renders the run summary. It reports cards and requirement records separately:
// they are different populations, and folding one into the other made the reported "card"
// count describe neither.
func summaryLine(result taskcheck.Result, violations, unverified int) string {
	checked := fmt.Sprintf("%d completed card(s)", result.Checked)
	if result.RequirementsChecked > 0 {
		checked += fmt.Sprintf(" and %d requirement record(s)", result.RequirementsChecked)
	}
	return fmt.Sprintf("taskcheck: %s checked, %d verified, %d violation(s), %d unverified",
		checked, result.Verified, violations, unverified)
}

// loadEvidence reads the git, gh and direct-commit evidence files. gh evidence is
// best-effort: a missing or malformed file degrades to local git evidence. The
// direct evidence file is not optional in the same sense -- the collector is
// this project's own script, so a file it cannot parse means the collection step
// is broken and must be reported rather than silently treated as "no evidence".
func loadEvidence(gitLog, ghPRs, directEvidence string) (*taskcheck.Evidence, error) {
	evidence := taskcheck.NewEvidence()
	if gitLog != "" {
		if err := withFile(gitLog, evidence.ParseGitLog); err != nil {
			return nil, err
		}
	}
	if ghPRs != "" {
		if err := withFile(ghPRs, evidence.ParseGHPullList); err != nil {
			fmt.Fprintf(os.Stderr, "taskcheck: warning: ignoring gh PR data: %v\n", err)
		}
	}
	if directEvidence != "" {
		if err := withFile(directEvidence, evidence.ParseDirectEvidence); err != nil {
			return nil, err
		}
	}
	return evidence, nil
}

// printDirectRefList writes `<repo>\t<sha>` for every `direct:` ref the cards
// cite, so the evidence collector can resolve them. It goes through the same
// ParseCard the gate uses: one parser, no second card-format implementation in
// shell.
func printDirectRefList(paths []string) {
	cards, err := loadCards(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "taskcheck: %v\n", err)
		os.Exit(2)
	}
	for _, ref := range taskcheck.DirectRefs(cards) {
		fmt.Printf("%s\t%s\n", ref.Repo, ref.SHA)
	}
}

func loadCards(paths []string) ([]taskcheck.Card, error) {
	cards := make([]taskcheck.Card, 0, len(paths))
	for _, path := range paths {
		card, err := taskcheck.ParseCard(path)
		if err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	return cards, nil
}

// loadRequirements parses every REQ card named by the -requirements spec: each
// whitespace-separated entry is either a directory of REQ-*.md files or a file.
func loadRequirements(spec string) ([]taskcheck.Requirement, error) {
	var reqs []taskcheck.Requirement
	for _, entry := range strings.Fields(spec) {
		paths := []string{entry}
		if info, err := os.Stat(entry); err == nil && info.IsDir() {
			matches, globErr := filepath.Glob(filepath.Join(entry, "REQ-*.md"))
			if globErr != nil {
				return nil, fmt.Errorf("glob %s: %w", entry, globErr)
			}
			paths = matches
		}
		for _, path := range paths {
			req, err := taskcheck.ParseRequirement(path)
			if err != nil {
				return nil, err
			}
			reqs = append(reqs, req)
		}
	}
	return reqs, nil
}

func withFile(path string, parse func(io.Reader) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if err := parse(f); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
