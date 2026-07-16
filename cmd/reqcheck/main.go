// Command reqcheck validates atomic requirement documents against the
// 10-section template (REQ-039).
//
// Usage:
//
//	reqcheck path/to/requirement.md [path/to/another.md ...]
package main

import (
	"fmt"
	"os"

	"github.com/ndzuki/release-manager/internal/quality/reqcheck"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: reqcheck <requirement.md>...\n")
		os.Exit(2)
	}

	exitCode := 0
	for _, path := range os.Args[1:] {
		result, err := reqcheck.Check(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reqcheck: %s: %v\n", path, err)
			exitCode = 1
			continue
		}

		if len(result.Violations) == 0 {
			fmt.Printf("%s: OK\n", path)
			continue
		}

		exitCode = 1
		for _, v := range result.Violations {
			fmt.Println(v.String())
		}
	}
	os.Exit(exitCode)
}
