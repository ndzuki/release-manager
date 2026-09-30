package deadmethods

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// readFile exists so the file-level plumbing stays in one place with its gosec justification:
// every path comes from the gate's own flags, not from user input.
func readFile(path string) ([]byte, error) {
	content, err := os.ReadFile(path) //nolint:gosec // repository-relative path from the gate's own flags
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return content, nil
}
