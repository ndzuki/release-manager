// Command taskpremises runs the premise-re-check audit: it reports `ready`
// planning/investigation task cards whose `verified_at`/`verified_head` record
// is missing, older than a threshold, or behind the current HEAD (see
// internal/quality/taskpremises).
//
// Usage:
//
//	taskpremises [-max N] [-max-age-days N] [-head <sha>] [-commits <file>] <card.md|dir>...
//
// The -commits file carries the injected commit counts, one `<sha>\t<count>` per
// line (a count of -1 means "sha unknown or not an ancestor of HEAD"). The
// Makefile target generates it with `git merge-base --is-ancestor` and
// `git rev-list --count`; this command stays free of os/exec so the SDK-only
// gate keeps passing.
//
// It is a read-only audit, NOT a gate: findings never change the exit status
// (0 whenever the audit ran). The heuristic and its blind spots are documented
// in the package comment. Exit status is 2 only when the audit itself could not
// run (usage error, unreadable card, unreadable -commits file).
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ndzuki/release-manager/internal/quality/taskpremises"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable seam. It parses flags, audits the named cards, and
// writes the summary to stdout. Findings never influence the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("taskpremises", flag.ContinueOnError)
	limit := flags.Int("max", 40, "print at most N findings")
	maxAgeDays := flags.Int("max-age-days", 14, "report cards whose verified_at is older than N days (<=0 disables)")
	head := flags.String("head", "", "current HEAD sha; empty skips the head comparison entirely")
	commitsFile := flags.String("commits", "",
		"file of sha<TAB>count lines precomputed with git merge-base --is-ancestor + git rev-list --count; count -1 means unknown or not-an-ancestor")

	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: taskpremises [-max N] [-max-age-days N] [-head <sha>] [-commits <file>] <card.md|dir>...")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	paths, err := expand(flags.Args())
	if err != nil {
		fmt.Fprintf(stderr, "taskpremises: %v\n", err)
		return 2
	}
	if len(paths) == 0 {
		fmt.Fprintln(stderr, "taskpremises: no task cards given")
		flags.Usage()
		return 2
	}

	cards := make([]taskpremises.Card, 0, len(paths))
	for _, path := range paths {
		card, parseErr := taskpremises.ParseCard(path)
		if parseErr != nil {
			fmt.Fprintf(stderr, "taskpremises: %v\n", parseErr)
			return 2
		}
		cards = append(cards, card)
	}

	counts, err := loadCommits(*commitsFile)
	if err != nil {
		fmt.Fprintf(stderr, "taskpremises: %v\n", err)
		return 2
	}

	report := taskpremises.Check(cards, taskpremises.Options{
		Now:          time.Now().UTC(),
		MaxAge:       time.Duration(*maxAgeDays) * 24 * time.Hour,
		Head:         strings.TrimSpace(*head),
		CommitsAhead: commitsAhead(*commitsFile, counts),
	})

	fmt.Fprintf(stdout, "taskpremises: %d card(s), %d ready, %d skipped (status != ready)\n",
		report.Cards, report.Checked, report.Skipped)
	fmt.Fprintf(stdout, "taskpremises: %d unrecorded, %d stale, %d head-moved, %d incomparable — read-only audit, not a gate\n",
		report.Count(taskpremises.KindUnrecorded),
		report.Count(taskpremises.KindStale),
		report.Count(taskpremises.KindHeadMoved),
		report.Count(taskpremises.KindIncomparable))
	if strings.TrimSpace(*head) == "" {
		fmt.Fprintln(stdout, "taskpremises: head comparison skipped (no -head given)")
	} else if *commitsFile == "" {
		fmt.Fprintln(stdout, "taskpremises: head comparison skipped (no -commits data given)")
	}

	shown := 0
	for _, finding := range report.Findings {
		if shown >= *limit {
			fmt.Fprintf(stdout, "taskpremises: ... %d more\n", len(report.Findings)-shown)
			break
		}
		fmt.Fprintln(stdout, finding.String())
		shown++
	}
	return 0
}

// expand turns the positional arguments into a sorted, de-duplicated card list.
// A directory argument contributes its TASK-*.md files, which lets the command
// be pointed at the vault Tasks directory directly.
func expand(args []string) ([]string, error) {
	seen := map[string]bool{}
	var paths []string
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", arg, err)
		}
		if !info.IsDir() {
			add(arg)
			continue
		}
		matches, err := filepath.Glob(filepath.Join(arg, "TASK-*.md"))
		if err != nil {
			return nil, fmt.Errorf("glob %s: %w", arg, err)
		}
		for _, match := range matches {
			add(match)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// loadCommits reads the `<sha>\t<count>` map. An empty path yields no data.
func loadCommits(path string) (map[string]int, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path) //nolint:gosec // trusted caller-provided file
	if err != nil {
		return nil, fmt.Errorf("open commits file %s: %w", path, err)
	}
	defer f.Close()

	counts := map[string]int{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		n, convErr := strconv.Atoi(fields[1])
		if convErr != nil {
			continue
		}
		counts[fields[0]] = n
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read commits file %s: %w", path, err)
	}
	return counts, nil
}

// commitsAhead builds the injected lookup. A sha the file does not name, or one
// whose count is negative, cannot be compared against HEAD.
func commitsAhead(path string, counts map[string]int) func(string) (int, bool) {
	if path == "" {
		return nil
	}
	return func(sha string) (int, bool) {
		n, ok := counts[strings.TrimSpace(sha)]
		if !ok || n < 0 {
			return 0, false
		}
		return n, true
	}
}
