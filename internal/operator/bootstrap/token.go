package bootstrap

import (
	"fmt"
	"os"
	"strings"
)

// resolveToken returns the enrollment token from the configured sources in
// precedence order: token file first, then environment variable (plan v1
// Step 6; REQ-065 injects the token via Secret → file/env in the agent
// deployment).
func resolveToken(cfg Config) (string, error) {
	if cfg.TokenFile != "" {
		token, err := readTokenFile(cfg.TokenFile)
		if err == nil {
			return token, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("read enrollment token file: %w", err)
		}
		// File configured but absent: fall through to the environment.
	}
	if cfg.TokenEnv != "" {
		if token := strings.TrimSpace(os.Getenv(cfg.TokenEnv)); token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("enrollment token not found (token file %q absent and environment %q empty)", cfg.TokenFile, cfg.TokenEnv)
}

// readTokenFile reads a single-line token file, trimming surrounding
// whitespace (0600 token files written by devseed / Secret mounts).
func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file %q is empty", path)
	}
	return token, nil
}
