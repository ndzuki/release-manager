// Command deadmethods runs the dead-concrete-method gate (see
// internal/quality/deadmethods). It exits 1 with the findings when an exported method on an
// unexported type is referenced nowhere and is not an interface member, when a registry entry no
// longer describes such a method, or when an entry has expired.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ndzuki/release-manager/internal/quality/deadmethods"
)

func main() {
	root := flag.String("root", ".", "repository root to scan")
	exceptions := flag.String("exceptions", "deadmethods.exceptions.yaml", "registry of knowingly-unreferenced methods")
	includeTests := flag.Bool("include-tests", false, "count references from test files too (default: shipping code only)")
	flag.Parse()

	report, err := deadmethods.Run(deadmethods.Options{
		Root:           *root,
		ExceptionsFile: *exceptions,
		IncludeTests:   *includeTests,
		Now:            time.Now().UTC(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "deadmethods: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("deadmethods: %d package(s), %d candidate method(s), %d without a reference or interface member, %d exception(s)\n",
		report.Packages, report.Candidates, len(report.Dead), report.Exceptions)
	for _, method := range report.Dead {
		fmt.Printf("  dead  %s", method.Key())
		if review, ok := report.ReviewDates[method.Key()]; ok {
			fmt.Printf("  (registered, review by %s)", review)
		}
		fmt.Println()
	}
	if len(report.Dynamic) > 0 {
		fmt.Printf("  note  %d method name(s) treated as alive via reflect MethodByName: %v\n", len(report.Dynamic), report.Dynamic)
	}
	if len(report.Findings) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\ndeadmethods: %d finding(s)\n", len(report.Findings))
	for _, finding := range report.Findings {
		fmt.Fprintf(os.Stderr, "  - %s\n", finding)
	}
	fmt.Fprintln(os.Stderr, "\nAn exported method on an unexported type that nothing references and no interface claims is either dead weight or a missing wire-up: delete it, wire it up, or register it in the exceptions file with a reason and a review date.")
	os.Exit(1)
}
