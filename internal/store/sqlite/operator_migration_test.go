package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/ndzuki/release-manager/internal/store"
)

// TestOpenMigratesLegacyTokenColumn rebuilds a legacy database whose
// enrollment_tokens table still carries the plaintext-capable `token` column
// (TASK-053 era: the column held only the hash) and verifies the REQ-015
// migration drops the column while keeping token lookups working.
func TestOpenMigratesLegacyTokenColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	plaintext := "legacy-token"
	legacyHash := sha256Hex(plaintext)
	_, err = db.ExecContext(ctx, `
CREATE TABLE enrollment_tokens (
	id TEXT PRIMARY KEY,
	customer_id TEXT NOT NULL,
	cluster_id TEXT NOT NULL,
	token TEXT NOT NULL UNIQUE,
	token_hash TEXT NOT NULL DEFAULT '',
	operator_name TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL DEFAULT 'pending',
	created_by_display_name TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	used_at TEXT,
	operator_id TEXT NOT NULL DEFAULT '',
	revoked_at TEXT,
	replaced_by_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE operators (
	id TEXT PRIMARY KEY,
	customer_id TEXT NOT NULL,
	cluster_id TEXT NOT NULL,
	operator_name TEXT NOT NULL DEFAULT '',
	cert_serial TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'active',
	superseded_by TEXT NOT NULL DEFAULT '',
	superseded_at TEXT,
	revoked_at TEXT,
	revoke_reason TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
`)
	require.NoError(t, err)
	// TASK-053-era write path: the legacy column holds only the hash.
	_, err = db.ExecContext(ctx, `
INSERT INTO enrollment_tokens (id, customer_id, cluster_id, token, token_hash, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, "token-1", "customer-1", "cluster-1", legacyHash, legacyHash,
		now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
INSERT INTO operators (id, customer_id, cluster_id, operator_name, cert_serial, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, "operator-1", "customer-1", "cluster-1", "legacy-operator", "00112233445566778899", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	st, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	token, err := st.EnrollmentTokens().GetByToken(ctx, plaintext)
	require.NoError(t, err)
	assert.Equal(t, store.TokenStatePending, token.State)
	assert.Equal(t, legacyHash, token.TokenHash)

	// The migrated schema must not retain the plaintext-capable column.
	rows, err := st.DB().QueryContext(ctx, `PRAGMA table_info(enrollment_tokens)`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		assert.NotEqual(t, "token", name, "migrated schema must not retain the plaintext token column")
	}
	require.NoError(t, rows.Err())

	// certificate_expires_at is writable on the migrated schema.
	expiresAt := now.Add(7 * 24 * time.Hour)
	require.NoError(t, st.OperatorLifecycle().UpdateCertificate(ctx, "operator-1", "00112233445566778899", "aabbccddeeff00112233", expiresAt))
	operator, err := st.Operators().Get(ctx, "operator-1")
	require.NoError(t, err)
	assert.Equal(t, "aabbccddeeff00112233", operator.CertSerial)
	require.NotNil(t, operator.CertificateExpiresAt)
	assert.Equal(t, expiresAt.UTC().Format(time.RFC3339Nano), operator.CertificateExpiresAt.UTC().Format(time.RFC3339Nano))
}
