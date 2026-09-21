package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeExceptions(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema-parity.exceptions.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: \"1\"\nexceptions:\n"+body), 0o600))
	return path
}

// An allowance must be owned, justified and dated -- the same contract
// errcodes.exceptions.yaml uses, so drift cannot be accepted forever.
func TestLoadExceptionsRequiresOwnerReasonAndExpiry(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"missing owner":  "  - table: \"t\"\n    column: \"c\"\n    reason: \"r\"\n    expires_at: \"2099-01-01\"\n",
		"missing reason": "  - owner: \"o\"\n    table: \"t\"\n    column: \"c\"\n    expires_at: \"2099-01-01\"\n",
		"missing expiry": "  - owner: \"o\"\n    table: \"t\"\n    column: \"c\"\n    reason: \"r\"\n",
	} {
		_, err := loadExceptions(writeExceptions(t, body), "2026-09-27")
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "needs an owner, a reason and an expires_at", name)
	}
}

// An expired exception stops excepting: the gate must fail rather than silently
// keep accepting drift the owner never renewed.
func TestLoadExceptionsRejectsExpiredEntries(t *testing.T) {
	t.Parallel()

	path := writeExceptions(t, "  - owner: \"o\"\n    table: \"t\"\n    column: \"c\"\n    reason: \"r\"\n    expires_at: \"2026-09-26\"\n")
	_, err := loadExceptions(path, "2026-09-27")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired on 2026-09-26")
}

func TestLoadExceptionsAcceptsAValidEntry(t *testing.T) {
	t.Parallel()

	path := writeExceptions(t, "  - owner: \"o\"\n    table: \"t\"\n    column: \"c\"\n    reason: \"r\"\n    expires_at: \"2026-12-31\"\n")
	got, err := loadExceptions(path, "2026-09-27")
	require.NoError(t, err)
	require.Contains(t, got, "t.c")
	assert.Equal(t, "2026-12-31", got["t.c"].ExpiresAt)
}
