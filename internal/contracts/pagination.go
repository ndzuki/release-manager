package contracts

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CursorOrder selects the sort direction for keyset predicates.
type CursorOrder int

const (
	// CursorDesc yields rows strictly before the cursor (newest-first pages).
	CursorDesc CursorOrder = iota
	// CursorAsc yields rows strictly after the cursor (oldest-first pages).
	CursorAsc
)

// EncodeCursor builds a base64-encoded cursor token from a timestamp and ID.
// Timestamp-based cursors provide stable pagination: records added after the
// cursor position appear in subsequent pages but don't shift existing results
// (AC-010-03).
func EncodeCursor(t time.Time, id string) string {
	payload := fmt.Sprintf("%d|%s", t.UnixNano(), id)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// DecodeCursor parses a cursor token back into its timestamp and ID components.
// Returns an error if the token is malformed or empty. Both the current
// UnixNano encoding and the legacy RFC3339Nano encoding (cursors minted before
// the shared helper) are accepted.
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

	if nanos, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
		return time.Unix(0, nanos).UTC(), parts[1], nil
	}

	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("contracts: invalid cursor timestamp: %w", err)
	}
	return ts.UTC(), parts[1], nil
}

// KeysetPredicate returns the SQL WHERE fragment for (created_at, id) keyset
// pagination in the given direction. The cursor row is strictly excluded, so
// records inserted during pagination land in later pages and never duplicate
// or drop rows of the snapshot (AC-010-03). Callers supply arguments in
// (created_at, created_at, id) order.
func KeysetPredicate(order CursorOrder) string {
	op := "<"
	if order == CursorAsc {
		op = ">"
	}
	return fmt.Sprintf("(created_at %s ? OR (created_at = ? AND id %s ?))", op, op)
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
