// Command storesurface runs the store-interface surface gate (see
// internal/quality/storesurface). It exits 1 with the findings when a method has no caller
// and no registered exception, when a registry entry no longer describes a dead method, or
// when an entry has expired.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ndzuki/release-manager/internal/quality/storesurface"
)

func main() {
	root := flag.String("root", ".", "repository root to scan")
	store := flag.String("store-package", "github.com/ndzuki/release-manager/internal/store", "import path of the package declaring the store interfaces")
	exceptions := flag.String("exceptions", "storesurface.exceptions.yaml", "registry of knowingly-dead methods")
	flag.Parse()

	report, err := storesurface.Run(storesurface.Options{
		Root:           *root,
		StorePackage:   *store,
		ExceptionsFile: *exceptions,
		Now:            time.Now().UTC(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "storesurface: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("storesurface: %d interface(s), %d declared method(s), %d not called through the interface, %d exception(s)\n",
		report.Interfaces, report.Declared, len(report.Dead), report.Exceptions)
	for _, method := range report.Dead {
		fmt.Printf("  dead  %s.%s", method.Interface, method.Name)
		if review, ok := report.ReviewDates[method.Interface+"."+method.Name]; ok {
			fmt.Printf("  (registered, review by %s)", review)
		}
		fmt.Println()
	}
	if len(report.Findings) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\nstoresurface: %d finding(s)\n", len(report.Findings))
	for _, finding := range report.Findings {
		fmt.Fprintf(os.Stderr, "  - %s\n", finding)
	}
	fmt.Fprintln(os.Stderr, "\nA method nobody calls through its interface is dead weight or a missing wire-up. Delete it, wire it, or register it in the exceptions file with a reason and a review date.")
	os.Exit(1)
}
