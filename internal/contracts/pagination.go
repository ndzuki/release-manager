package contracts

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EncodeCursor builds a base64-encoded cursor token from a timestamp and ID.
// Timestamp-based cursors provide stable pagination: records added after the
// cursor position appear in subsequent pages but don't shift existing results.
func EncodeCursor(t time.Time, id string) string {
	payload := fmt.Sprintf("%d|%s", t.UnixNano(), id)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// DecodeCursor parses a cursor token back into its timestamp and ID components.
// Returns an error if the token is malformed or empty.
func DecodeCursor(token string) (time.Time, string, error) {
	if token == "" {
		return time.Time{}, "", fmt.Errorf("contracts: empty cursor token")
	}

	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("contracts: invalid cursor encoding: %w", err)
	}

	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("contracts: malformed cursor: expected 'ts|id'")
	}

	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("contracts: invalid cursor timestamp: %w", err)
	}

	return time.Unix(0, nanos), parts[1], nil
}

const (
	minPageSize = 1
	maxPageSize = 100
)

// NormalizePageSize clamps n to the valid range [1, 100].
// Zero or negative values default to 20.
func NormalizePageSize(n int32) int32 {
	if n <= 0 {
		return 20
	}
	if n > maxPageSize {
		return maxPageSize
	}
	return n
}
