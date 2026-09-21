// Command taskcheck validates the delivery ledger (vault Tasks/*.md) against the
// merge facts in git.
//
// Usage:
//
//	taskcheck -git-log <file> [-gh-prs <file>] [-allow-unverified] <TASK-*.md>...
//
// The Makefile collects the evidence because this tool must not import
// os/exec (the SDK-only gate scans cmd/ and internal/): `git log --all
// --format=%H%x09%s` feeds -git-log, and `gh pr list --state all --limit <n>
// --json number,state,mergeCommit` feeds -gh-prs when gh is available.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ndzuki/release-manager/internal/quality/taskcheck"
)

func main() {
	var (
		gitLog          string
		ghPRs           string
		allowUnverified bool
	)
	flag.StringVar(&gitLog, "git-log", "", "path to `git log --all --format=%H%x09%s` output")
	flag.StringVar(&ghPRs, "gh-prs", "", "path to `gh pr list --json number,state,mergeCommit` output (optional)")
	flag.BoolVar(&allowUnverified, "allow-unverified", false, "report UNVERIFIED cards without failing (ALLOW_UNVERIFIED_TASKS=1)")
	flag.Parse()

	paths := flag.Args()
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: taskcheck -git-log <file> [-gh-prs <file>] [-allow-unverified] <TASK-*.md>...")
		os.Exit(2)
	}

	evidence := taskcheck.NewEvidence()
	if gitLog != "" {
		if err := withFile(gitLog, evidence.ParseGitLog); err != nil {
			fmt.Fprintf(os.Stderr, "taskcheck: %v\n", err)
			os.Exit(2)
		}
	}
	if ghPRs != "" {
		if err := withFile(ghPRs, evidence.ParseGHPullList); err != nil {
			// gh evidence is best-effort: a missing or malformed file degrades
			// to local git evidence rather than failing the run outright.
			fmt.Fprintf(os.Stderr, "taskcheck: warning: ignoring gh PR data: %v\n", err)
		}
	}

	cards := make([]taskcheck.Card, 0, len(paths))
	for _, path := range paths {
		card, err := taskcheck.ParseCard(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "taskcheck: %v\n", err)
			os.Exit(2)
		}
		cards = append(cards, card)
	}

	opts := taskcheck.Options{AllowUnverified: allowUnverified}
	result := taskcheck.Check(cards, evidence)
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

	fmt.Printf("taskcheck: %d completed card(s) checked, %d verified, %d violation(s), %d unverified\n",
		result.Checked, result.Verified, violations, unverified)
	if result.Failed(opts) {
		if violations == 0 && unverified > 0 {
			fmt.Fprintln(os.Stderr, "taskcheck: set ALLOW_UNVERIFIED_TASKS=1 to accept unverified merge evidence explicitly")
		}
		os.Exit(1)
	}
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
