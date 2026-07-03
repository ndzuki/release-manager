//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// buildAndLoadImage builds a Go binary, builds a Docker image, and loads it into a kind cluster.
func buildAndLoadImage(ctx context.Context, clusterName, binaryName, imageTag, dockerfile string) error {
	root := projectRoot()

	buildCmd := exec.CommandContext(ctx, "go", "build", "-ldflags=-s -w",
		"-o", "bin/"+binaryName, "./cmd/"+binaryName+"/")
	buildCmd.Dir = root
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build %s: %w\n%s", binaryName, err, string(out))
	}

	dockerCmd := exec.CommandContext(ctx, "docker", "build",
		"-f", dockerfile, "-t", imageTag, ".")
	dockerCmd.Dir = root
	if out, err := dockerCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker build %s: %w\n%s", imageTag, err, string(out))
	}

	loadCmd := exec.CommandContext(ctx, "kind", "load", "docker-image",
		imageTag, "--name", clusterName)
	if out, err := loadCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kind load %s: %w\n%s", imageTag, err, string(out))
	}
	return nil
}
