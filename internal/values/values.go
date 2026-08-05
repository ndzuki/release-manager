// Package values validates, canonicalizes, and digests immutable Helm values documents.
package values

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var (
	ErrInvalidYAML      = errors.New("invalid YAML")
	ErrSizeExceeded     = errors.New("size exceeded")
	ErrSecretLiteral    = errors.New("secret literal forbidden")
	ErrInvalidSecretRef = errors.New("invalid secret reference")
)

const maxSecretRefs = 64

var secretKeyPattern = regexp.MustCompile(`^[-_.a-zA-Z0-9]+$`)

// SecretRef identifies a cluster-local Secret value injected at one JSON Pointer.
type SecretRef struct {
	Path string
	Name string
	Key  string
}

// CanonicalResult is the validated immutable document representation.
type CanonicalResult struct {
	Canonical []byte
	Digest    string
}

// Canonicalize parses exactly one YAML or JSON document into deterministic JSON.
func Canonicalize(input []byte) ([]byte, error) {
	document, err := parseDocument(input)
	if err != nil {
		return nil, err
	}
	return marshalCanonical(document)
}

// Digest returns the lowercase SHA-256 hex digest of canonical JSON.
func Digest(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// Validate preserves the legacy validation seam without SecretRef inputs.
func Validate(input []byte, maxSize int64) (*CanonicalResult, error) {
	return ValidateWithRefs(input, maxSize, nil, nil)
}

// ValidateWithRefs enforces size, parse, Secret literal, canonicalization, and SecretRef rules.
func ValidateWithRefs(input []byte, maxSize int64, secretPatterns []string, refs []SecretRef) (*CanonicalResult, error) {
	if maxSize > 0 && int64(len(input)) > maxSize {
		return nil, ErrSizeExceeded
	}
	document, err := parseDocument(input)
	if err != nil {
		return nil, err
	}
	if containsSecret(document, secretPatterns) {
		return nil, ErrSecretLiteral
	}
	if err := validateSecretRefs(document, refs); err != nil {
		return nil, err
	}
	canonical, err := marshalCanonical(document)
	if err != nil {
		return nil, err
	}
	return &CanonicalResult{Canonical: canonical, Digest: Digest(canonical)}, nil
}

// ContainsSecret reports whether a valid document contains a likely literal secret.
// Invalid input fails closed.
func ContainsSecret(input []byte) bool {
	document, err := parseDocument(input)
	return err != nil || containsSecret(document, nil)
}

func validateSecretRefs(document any, refs []SecretRef) error {
	if len(refs) > maxSecretRefs {
		return fmt.Errorf("%w: at most %d references are allowed", ErrInvalidSecretRef, maxSecretRefs)
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Path == "" || len(k8svalidation.IsDNS1123Subdomain(ref.Name)) != 0 ||
			ref.Key == "" || len(ref.Key) > 63 || !secretKeyPattern.MatchString(ref.Key) {
			return fmt.Errorf("%w: invalid path, name, or key", ErrInvalidSecretRef)
		}
		if _, exists := seen[ref.Path]; exists {
			return fmt.Errorf("%w: duplicate path", ErrInvalidSecretRef)
		}
		seen[ref.Path] = struct{}{}
		value, exists, err := resolvePointer(document, ref.Path)
		if err != nil || !exists || value != nil {
			return fmt.Errorf("%w: path must exist and contain null", ErrInvalidSecretRef)
		}
	}
	return nil
}

func stripBOM(input []byte) []byte {
	if len(input) >= 3 && input[0] == 0xEF && input[1] == 0xBB && input[2] == 0xBF {
		return input[3:]
	}
	return input
}

// IsYAMLError reports whether err is an invalid document error.
func IsYAMLError(err error) bool {
	return errors.Is(err, ErrInvalidYAML) || strings.Contains(err.Error(), ErrInvalidYAML.Error())
}
