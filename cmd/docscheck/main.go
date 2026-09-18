// Command docscheck audits code citations in documentation: when the sentence
// next to a `file:line` citation names a symbol, the symbol should appear within
// a small window around the cited line.
//
// Usage:
//
//	docscheck [-root .] [-max N]
//
// It is a read-only audit, not a gate: the rule's false-positive rate on this
// corpus is high (see the docscheck package comment), so the findings go to a
// human. Exit status is always 0 unless the audit itself fails.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ndzuki/release-manager/internal/quality/docscheck"
)

func main() {
	root := flag.String("root", ".", "repository root")
	limit := flag.Int("max", 40, "print at most N findings")
	flag.Parse()

	result, err := docscheck.Audit(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docscheck: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("docscheck: %d markdown files, %d citations, %d with a named symbol\n",
		result.Files, result.Citations, result.Checked)
	fmt.Printf("docscheck: %d findings (window ±%d) — read-only audit, not a gate\n",
		len(result.Findings), docscheck.Window)

	shown := 0
	for _, finding := range result.Findings {
		if shown >= *limit {
			fmt.Printf("docscheck: ... %d more\n", len(result.Findings)-shown)
			break
		}
		fmt.Println(finding.String())
		shown++
	}
}
