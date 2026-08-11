// Command sdkcheck runs the SDK-only static analyzer on a Go module.
// It detects os/exec calls to Helm, kubectl, istioctl, and other forbidden
// binaries that violate the SDK-only policy (REQ-037).
//
// Usage:
//
//	sdkcheck [-exceptions sdkcheck.exceptions.yaml] ./...
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ndzuki/release-manager/internal/quality/sdkcheck"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	exceptionsPath := flag.String("exceptions", "sdkcheck.exceptions.yaml", "path to exceptions YAML file")
	buildTags := flag.String("build-tags", "", "comma-separated build tags (e.g., 'integration')")
	flag.Parse()

	a, err := sdkcheck.NewAnalyzer(*exceptionsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sdkcheck: %v\n", err)
		os.Exit(2)
	}

	if *buildTags != "" {
		orig := os.Getenv("GOFLAGS")
		t := "-tags=" + *buildTags
		if orig != "" {
			t = orig + " " + t
		}
		os.Setenv("GOFLAGS", t)
	}

	singlechecker.Main(a)
}
