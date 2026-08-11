package localstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBoltStore_IdentityRoundTrip: identity survives a real reopen of the
// BoltDB file (AC-075-02: agent restart reconnects with the persisted identity
// instead of re-enrolling).
func TestBoltStore_IdentityRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := OpenBolt(path)
	require.NoError(t, err)

	identity := &Identity{
		OperatorID:     "op-1",
		OperatorName:   "cluster-1",
		CustomerID:     "cust-1",
		ClusterID:      "cluster-1",
		SessionID:      "sess-1",
		PrivateKeyPEM:  "-----BEGIN PRIVATE KEY-----\nopaque\n-----END PRIVATE KEY-----\n",
		CertificatePEM: "-----BEGIN CERTIFICATE-----\nopaque\n-----END CERTIFICATE-----\n",
	}
	require.NoError(t, store.SaveIdentity(ctx, identity))

	// Close and reopen the same file: the identity must be readable.
	require.NoError(t, store.Close())
	reopened, err := OpenBolt(path)
	require.NoError(t, err)
	defer reopened.Close()

	got, err := reopened.LoadIdentity(ctx)
	require.NoError(t, err)
	assert.Equal(t, identity, got)
}

// TestBoltStore_IdentityNotFound: an untouched store reports ErrNotFound so
// the bootstrap path knows it must enroll.
func TestBoltStore_IdentityNotFound(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := OpenBolt(path)
	require.NoError(t, err)
	defer store.Close()

	_, err = store.LoadIdentity(ctx)
	require.ErrorIs(t, err, ErrNotFound)
}

// TestBoltStore_IdentityOverwrite: saving twice replaces the previous identity
// (single-key semantics — one operator identity per agent).
func TestBoltStore_IdentityOverwrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := OpenBolt(path)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.SaveIdentity(ctx, &Identity{OperatorID: "op-1"}))
	require.NoError(t, store.SaveIdentity(ctx, &Identity{OperatorID: "op-2"}))
	got, err := store.LoadIdentity(ctx)
	require.NoError(t, err)
	assert.Equal(t, "op-2", got.OperatorID)
}

// TestBoltStore_FilePermissions: the database file is created 0600 so the
// private key material inside it is not world-readable.
func TestBoltStore_FilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := OpenBolt(path)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
