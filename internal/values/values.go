// Package values provides validation, canonicalization, and digest
// computation for Helm values documents.
package values

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// Errors returned by validation.
var (
	ErrInvalidYAML   = fmt.Errorf("invalid YAML")
	ErrSizeExceeded  = fmt.Errorf("size exceeded")
	ErrSecretLiteral = fmt.Errorf("secret literal forbidden")
)

// CanonicalResult holds the output of a successful validation pass.
type CanonicalResult struct {
	Canonical []byte // canonical JSON, sorted keys
	Digest    string // hex-encoded SHA-256 of Canonical
}

// Canonicalize parses YAML or JSON input and returns a deterministic
// canonical JSON representation. Keys are sorted by encoding/json.
func Canonicalize(input []byte) ([]byte, error) {
	input = stripBOM(input)
	if len(input) == 0 {
		return []byte("{}"), nil
	}

	var node any
	if err := yaml.Unmarshal(input, &node); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidYAML, err)
	}

	out, err := json.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("canonical marshal: %w", err)
	}
	return out, nil
}

// Digest returns the SHA-256 hex digest of canonical JSON.
func Digest(canonical []byte) string {
	h := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", h)
}

// Validate parses input, checks size and secret patterns,
// canonicalizes, and computes the digest.
//
// maxSize is the maximum input size in bytes. 0 means no limit.
func Validate(input []byte, maxSize int64) (*CanonicalResult, error) {
	if maxSize > 0 && int64(len(input)) > maxSize {
		return nil, ErrSizeExceeded
	}

	if ContainsSecret(input) {
		return nil, ErrSecretLiteral
	}

	canonical, err := Canonicalize(input)
	if err != nil {
		return nil, err
	}

	return &CanonicalResult{
		Canonical: canonical,
		Digest:    Digest(canonical),
	}, nil
}

// stripBOM removes a UTF-8 byte order mark if present.
func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// IsYAMLError returns true if err wraps ErrInvalidYAML.
func IsYAMLError(err error) bool {
	return strings.Contains(err.Error(), ErrInvalidYAML.Error())
}
