// Command errcodecheck checks that every stable error code a requirement asserts
// in its acceptance criteria is emittable by the implementation.
//
// Usage:
//
//	errcodecheck -repo . -exceptions errcodes.exceptions.yaml REQ-*.md ...
//
// The requirement documents live in the knowledge base, not in this repository,
// so a CI checkout cannot see them. Point the command at the vault instead:
//
//	errcodecheck -repo . -exceptions errcodes.exceptions.yaml \
//	    ~/src/repos/github.com/ndzuki/myNote/Projects/001-release-manager/Requirements/REQ-*.md
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ndzuki/release-manager/internal/quality/errcodes"
)

type exceptionFile struct {
	Version    string      `yaml:"version"`
	Exceptions []exception `yaml:"exceptions"`
}

type exception struct {
	Owner     string `yaml:"owner"`
	Reason    string `yaml:"reason"`
	ExpiresAt string `yaml:"expires_at"`
	Code      string `yaml:"code"`
}

func main() {
	repo := flag.String("repo", ".", "repository root searched for emitters")
	exceptionsPath := flag.String("exceptions", "", "YAML file listing accepted unemitted codes")
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "usage: errcodecheck [-repo dir] [-exceptions file] <requirement.md>...\n")
		os.Exit(2)
	}

	exceptions, err := loadExceptions(*exceptionsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "errcodecheck: %v\n", err)
		os.Exit(1)
	}

	exitCode := 0
	checked := 0
	for _, path := range flag.Args() {
		result, err := errcodes.Check(path, errcodes.Options{RepoRoot: *repo, Exceptions: exceptions})
		if err != nil {
			fmt.Fprintf(os.Stderr, "errcodecheck: %s: %v\n", path, err)
			exitCode = 1
			continue
		}
		checked += result.CodesChecked
		if len(result.Violations) == 0 {
			continue
		}
		exitCode = 1
		for _, violation := range result.Violations {
			fmt.Println(violation.String())
		}
	}

	if exitCode == 0 {
		fmt.Printf("errcodecheck: %d asserted error codes are emittable (%d exceptions)\n", checked, len(exceptions))
	}
	os.Exit(exitCode)
}

func loadExceptions(path string) (map[string]string, error) {
	if path == "" {
		return map[string]string{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file exceptionFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	out := make(map[string]string, len(file.Exceptions))
	for _, e := range file.Exceptions {
		if e.Code == "" {
			return nil, fmt.Errorf("%s: exception without a code", path)
		}
		if e.Reason == "" || e.ExpiresAt == "" {
			return nil, fmt.Errorf("%s: exception %q needs a reason and an expires_at", path, e.Code)
		}
		// An expired exception stops excepting: the code is checked again and the
		// gate fails until someone re-justifies or fixes it.
		if e.ExpiresAt < today {
			fmt.Fprintf(os.Stderr, "errcodecheck: exception for %q expired on %s (%s)\n", e.Code, e.ExpiresAt, e.Reason)
			continue
		}
		out[e.Code] = e.Reason
	}
	return out, nil
}
