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
	"path/filepath"
	"strings"

	"github.com/ndzuki/release-manager/internal/quality/taskcheck"
)

func main() {
	var (
		gitLog          string
		ghPRs           string
		requirements    string
		allowUnverified bool
	)
	flag.StringVar(&gitLog, "git-log", "", "path to `git log --all --format=%H%x09%s` output")
	flag.StringVar(&ghPRs, "gh-prs", "", "path to `gh pr list --json number,state,mergeCommit` output (optional)")
	flag.StringVar(&requirements, "requirements", "", "vault Requirements directory or REQ-*.md file list (optional: enables the REQ delivery-ledger check)")
	flag.BoolVar(&allowUnverified, "allow-unverified", false, "report UNVERIFIED cards without failing (ALLOW_UNVERIFIED_TASKS=1)")
	flag.Parse()

	paths := flag.Args()
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: taskcheck -git-log <file> [-gh-prs <file>] [-allow-unverified] <TASK-*.md>...")
		os.Exit(2)
	}

	evidence, err := loadEvidence(gitLog, ghPRs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "taskcheck: %v\n", err)
		os.Exit(2)
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
		reqResult := taskcheck.CheckRequirements(reqs)
		result.Findings = append(result.Findings, reqResult.Findings...)
		result.Checked += reqResult.Checked
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

	fmt.Printf("taskcheck: %d completed card(s) checked, %d verified, %d violation(s), %d unverified\n",
		result.Checked, result.Verified, violations, unverified)
	if result.Failed(opts) {
		if violations == 0 && unverified > 0 {
			fmt.Fprintln(os.Stderr, "taskcheck: set ALLOW_UNVERIFIED_TASKS=1 to accept unverified merge evidence explicitly")
		}
		os.Exit(1)
	}
}

// loadEvidence reads the git and gh evidence files. gh evidence is best-effort:
// a missing or malformed file degrades to local git evidence.
func loadEvidence(gitLog, ghPRs string) (*taskcheck.Evidence, error) {
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
	return evidence, nil
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
