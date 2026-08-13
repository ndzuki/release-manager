//go:build integration

package postgres_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// Trust root store contract tests (REQ-043). These mirror the sqlite suite and
// additionally exercise transaction atomicity of BumpPolicy / BumpRevocationEpoch
// against a real PostgreSQL server (POSTGRES_TEST_DSN, skipped when unset).
// Per extended/databases/sql-guide.md: transactions must be atomic; concurrent
// bumps must not lose updates.

func TestTrustRootCreateAndGet(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	root := &store.TrustRoot{
		ID:           uuid.NewString(),
		Environment:  "staging",
		KeyID:        "k1",
		PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEAtest\n-----END PUBLIC KEY-----",
		Issuer:       "release-manager-ci",
		State:        store.TrustRootActive,
		ValidFrom:    now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	require.NoError(t, st.TrustRoots().Create(ctx, root))

	got, err := st.TrustRoots().Get(ctx, root.ID)
	require.NoError(t, err)
	assert.Equal(t, root.ID, got.ID)
	assert.Equal(t, store.TrustRootActive, got.State)
	assert.Equal(t, "release-manager-ci", got.Issuer)
}

func TestTrustRootGetNotFound(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	_, err := st.TrustRoots().Get(ctx, "nonexistent")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestTrustRootListByEnvironment(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	for _, id := range []string{uuid.NewString(), uuid.NewString(), uuid.NewString()} {
		require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
			ID: id, Environment: "staging", KeyID: uuid.NewString(), Issuer: "ci-" + id,
			State: store.TrustRootActive, ValidFrom: now,
			CreatedAt: now, UpdatedAt: now,
		}))
	}
	// different environment
	require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
		ID: uuid.NewString(), Environment: "production", KeyID: uuid.NewString(), Issuer: "ci-prod",
		State: store.TrustRootActive, ValidFrom: now,
		CreatedAt: now, UpdatedAt: now,
	}))

	roots, err := st.TrustRoots().ListByEnvironment(ctx, "staging")
	require.NoError(t, err)
	assert.Len(t, roots, 3)

	roots, err = st.TrustRoots().ListByEnvironment(ctx, "production")
	require.NoError(t, err)
	assert.Len(t, roots, 1)
}

func TestTrustRootGetActiveByEnvironment(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)

	// Active root
	require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
		ID: uuid.NewString(), Environment: "staging", KeyID: uuid.NewString(), Issuer: "ci-1",
		State: store.TrustRootActive, ValidFrom: past,
		CreatedAt: now, UpdatedAt: now,
	}))
	// Grace root within window
	graceUntil := now.Add(1 * time.Hour)
	require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
		ID: uuid.NewString(), Environment: "staging", KeyID: uuid.NewString(), Issuer: "ci-2",
		State: store.TrustRootGrace, ValidFrom: past, GraceUntil: &graceUntil,
		CreatedAt: now, UpdatedAt: now,
	}))
	// Retired root — should not appear
	require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
		ID: uuid.NewString(), Environment: "staging", KeyID: uuid.NewString(), Issuer: "ci-3",
		State: store.TrustRootRetired, ValidFrom: past,
		CreatedAt: now, UpdatedAt: now,
	}))

	active, err := st.TrustRoots().GetActiveByEnvironment(ctx, "staging", now)
	require.NoError(t, err)
	assert.Len(t, active, 2) // active + grace
}

func TestTrustRootUpdate(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	root := &store.TrustRoot{
		ID: uuid.NewString(), Environment: "staging", KeyID: uuid.NewString(), Issuer: "ci-1",
		State: store.TrustRootPending, ValidFrom: now,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.TrustRoots().Create(ctx, root))

	root.State = store.TrustRootActive
	require.NoError(t, st.TrustRoots().Update(ctx, root))

	got, err := st.TrustRoots().Get(ctx, root.ID)
	require.NoError(t, err)
	assert.Equal(t, store.TrustRootActive, got.State)
}

func TestTrustRootBumpPolicy(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	// First bump initializes policy lazily.
	ver, epoch, err := st.TrustRoots().BumpPolicy(ctx, "staging")
	require.NoError(t, err)
	assert.Equal(t, int64(1), ver)
	assert.Equal(t, int64(0), epoch)

	// Second bump increments.
	ver, _, err = st.TrustRoots().BumpPolicy(ctx, "staging")
	require.NoError(t, err)
	assert.Equal(t, int64(2), ver)
}

func TestTrustRootBumpRevocationEpoch(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	epoch, err := st.TrustRoots().BumpRevocationEpoch(ctx, "production")
	require.NoError(t, err)
	assert.Equal(t, int64(1), epoch)

	epoch, err = st.TrustRoots().BumpRevocationEpoch(ctx, "production")
	require.NoError(t, err)
	assert.Equal(t, int64(2), epoch)
}

func TestTrustRootGetPolicyDefault(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	// No policy row yet → returns lazy defaults (REQ-043: read semantics version=1/epoch=0).
	meta, err := st.TrustRoots().GetPolicy(ctx, "unknown-env")
	require.NoError(t, err)
	assert.Equal(t, int64(1), meta.Version)
	assert.Equal(t, int64(0), meta.RevocationEpoch)
}

// TestTrustRootBumpPolicyConcurrent verifies the BumpPolicy transaction does not
// lose updates under concurrent writers (REQ-043: policy version must be strictly
// monotonic; AC-043-03 relies on atomic increments).
func TestTrustRootBumpPolicyConcurrent(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := st.TrustRoots().BumpPolicy(ctx, "concurrent-env")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	meta, err := st.TrustRoots().GetPolicy(ctx, "concurrent-env")
	require.NoError(t, err)
	assert.Equal(t, int64(workers), meta.Version, "no lost update: version must equal bump count")
	assert.Equal(t, int64(0), meta.RevocationEpoch)
}

// TestTrustRootBumpRevocationEpochConcurrent mirrors the policy test for the
// revocation epoch (AC-043-04 depends on epoch monotonicity for cache invalidation).
func TestTrustRootBumpRevocationEpochConcurrent(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.TrustRoots().BumpRevocationEpoch(ctx, "concurrent-env")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	meta, err := st.TrustRoots().GetPolicy(ctx, "concurrent-env")
	require.NoError(t, err)
	assert.Equal(t, int64(workers), meta.RevocationEpoch, "no lost update: epoch must equal bump count")
	// BumpRevocationEpoch only touches the epoch; the lazy bootstrap row keeps
	// version 0 until a BumpPolicy happens (REQ-043: revoke bumps epoch, not version).
	assert.Equal(t, int64(0), meta.Version)
}
