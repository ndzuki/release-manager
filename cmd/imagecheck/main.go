// Command imagecheck validates a Docker image archive against an executable policy.
//
// Usage:
//
//	imagecheck --archive <tarball> --policy <policy.yaml> --dockerfile <Dockerfile>
//
// Exit 0 = compliant, 1 = policy violation, 2 = input/parse error.
//
// ARCHIVE: path to docker image save tarball, or "-" for stdin.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ndzuki/release-manager/internal/quality/imagecheck"
)

func main() {
	dockerfileFlag := flag.String("dockerfile", "deploy/docker/Dockerfile.operator", "path to Dockerfile")
	policyFlag := flag.String("policy", "imagecheck.operator.yaml", "path to policy YAML file")
	archiveFlag := flag.String("archive", "-", "path to docker image save tarball (\"-\" for stdin)")
	flag.Parse()

	if *archiveFlag == "" {
		fmt.Fprintf(os.Stderr, "imagecheck: --archive is required\n")
		os.Exit(2)
	}
	if *dockerfileFlag == "" {
		fmt.Fprintf(os.Stderr, "imagecheck: --dockerfile is required\n")
		os.Exit(2)
	}
	if *policyFlag == "" {
		fmt.Fprintf(os.Stderr, "imagecheck: --policy is required\n")
		os.Exit(2)
	}

	policy, err := imagecheck.LoadPolicy(*policyFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "imagecheck: %v\n", err)
		os.Exit(2)
	}

	readerCloser, err := imagecheck.ArchiveFile(*archiveFlag, os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "imagecheck: open archive: %v\n", err)
		os.Exit(2)
	}
	defer readerCloser.Close()

	result, err := imagecheck.Analyze(readerCloser, *dockerfileFlag, policy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "imagecheck: %v\n", err)
		os.Exit(2)
	}

	if result.Passed() {
		return
	}

	for _, violation := range result.Violations {
		data, err := imagecheck.MarshalViolation(violation)
		if err != nil {
			fmt.Fprintf(os.Stderr, "imagecheck: marshal violation: %v\n", err)
			os.Exit(2)
		}
		fmt.Println(string(data))
	}
	os.Exit(1)
}
