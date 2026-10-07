// Command docfacts audits documentation claims that can be re-derived from the
// configuration they describe (TASK-254). It is a read-only audit, not a gate: it
// always exits 0, and `make quality` does not run it. Run it with `make
// audit-doc-facts`.
package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ndzuki/release-manager/internal/quality/docfacts"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the test seam: it returns the process exit code and writes the report.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("docfacts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root")
	limit := flags.Int("max", 40, "print at most N findings (0 prints all)")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	nginxPath := filepath.Join(*root, "web", "nginx.conf")
	vitePath := filepath.Join(*root, "web", "vite.config.ts")
	hostPath := filepath.Join(*root, "deploy", "dev", "lib", "host.sh")
	nginxConf, err := os.ReadFile(nginxPath)
	if err != nil {
		fmt.Fprintf(stderr, "docfacts: %v\n", err)
		return 2
	}
	viteConfig, err := os.ReadFile(vitePath)
	if err != nil {
		fmt.Fprintf(stderr, "docfacts: %v\n", err)
		return 2
	}
	hostSh, err := os.ReadFile(hostPath)
	if err != nil {
		fmt.Fprintf(stderr, "docfacts: %v\n", err)
		return 2
	}
	facts := docfacts.NewFacts(string(nginxConf), string(viteConfig), string(hostSh), "deploy/dev/lib/host.sh")

	items, err := collect(*root)
	if err != nil {
		fmt.Fprintf(stderr, "docfacts: %v\n", err)
		return 2
	}
	findings := docfacts.Check(items, facts)

	fmt.Fprintf(stdout, "docfacts: %d markdown line(s) scanned; nginx forwards %d package prefix(es), vite %d, host port band %d-%d\n",
		len(items), len(facts.NginxPackagePrefixes), len(facts.VitePrefixes), facts.HostPortLow, facts.HostPortHigh)
	if len(findings) == 0 {
		fmt.Fprintf(stdout, "docfacts: no claim contradicts its source of truth — read-only audit, not a gate\n")
		return 0
	}
	shown := findings
	if *limit > 0 && len(shown) > *limit {
		shown = shown[:*limit]
	}
	for _, finding := range shown {
		fmt.Fprintln(stdout, finding.String())
	}
	if rest := len(findings) - len(shown); rest > 0 {
		fmt.Fprintf(stdout, "docfacts: ... %d more\n", rest)
	}
	fmt.Fprintf(stdout, "docfacts: %d finding(s) — read-only audit, not a gate\n", len(findings))
	return 0
}

// skipDirs are trees that never hold hand-written documentation.
var skipDirs = map[string]bool{
	".git": true, ".worktrees": true, "node_modules": true, "data": true,
	"bin": true, "e2e-results": true, "dist": true,
}

// collect reads every markdown file under root into rule items, keeping each
// line's previous non-empty line so a qualifier written above a claim counts.
func collect(root string) ([]docfacts.Item, error) {
	// The walk only gathers paths; the reads happen afterwards, outside the
	// callback, so a symlink swap cannot change what a path resolved to between the
	// directory entry and the read (gosec G122).
	paths := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipDirs[entry.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".md") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	items := []docfacts.Item{}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		// context keeps the last few non-empty lines: a historical qualifier is often
		// written a line or two above the claim it covers.
		context := []string{}
		for i, text := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				continue
			}
			items = append(items, docfacts.Item{Path: rel, Line: i + 1, Text: trimmed, PrevText: strings.Join(context, "\n")})
			context = append(context, trimmed)
			if len(context) > 3 {
				context = context[len(context)-3:]
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Path != items[j].Path {
			return items[i].Path < items[j].Path
		}
		return items[i].Line < items[j].Line
	})
	return items, nil
}
