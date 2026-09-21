package localstore

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TASK-156: a second process on the same store must fail in bounded time with a
// diagnosable error instead of blocking forever inside bolt.Open.
//
// The deadlock this locks out: a RollingUpdate brought up a second agent Pod on
// the shared ReadWriteOnce operator-identity volume; bbolt's zero Timeout means
// "wait forever", so the new Pod blocked before it could log anything, never
// became Ready, and the old Pod was never replaced — the rollout deadlocked and
// only a manual scale 0→1 recovered it.
func TestOpenBoltFailsInBoundedTimeWhenAnotherProcessHoldsTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-commands.db")
	first, err := OpenBolt(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })

	// The production wait is seconds; the timeout path is what is under test.
	restore := boltLockTimeout
	boltLockTimeout = 250 * time.Millisecond
	t.Cleanup(func() { boltLockTimeout = restore })

	type openResult struct {
		store *BoltStore
		err   error
	}
	done := make(chan openResult, 1)
	start := time.Now()
	go func() {
		store, err := OpenBolt(path)
		done <- openResult{store: store, err: err}
	}()

	select {
	case got := <-done:
		require.Error(t, got.err, "the second open must not succeed while the store is locked")
		assert.Nil(t, got.store)
		assert.ErrorIs(t, got.err, ErrStoreLocked,
			"the error must say the store is locked, not just carry a bbolt error")
		assert.Contains(t, got.err.Error(), path, "the error must name the store path")
		assert.Less(t, time.Since(start), 10*time.Second, "the lock wait must be bounded")
	case <-time.After(10 * time.Second):
		t.Fatal("second OpenBolt never returned: the lock wait is unbounded (TASK-156)")
	}
}

// TASK-156: an unlocked store still opens normally — the timeout must not turn
// into a spurious failure.
func TestOpenBoltSucceedsWhenTheStoreIsFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-commands.db")

	store, err := OpenBolt(path)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	reopened, err := OpenBolt(path)
	require.NoError(t, err, "a closed store must be openable again")
	require.NoError(t, reopened.Close())
}
