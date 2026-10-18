package auth

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// applyErroringStore returns a nil snapshot (plus an error) from Apply, which is
// the contract both SQLite and PostgreSQL implement on every failure path.
type applyErroringStore struct{ store.Store }

func (s applyErroringStore) Authorization() store.AuthorizationStore {
	return applyErroringAuthorization{AuthorizationStore: s.Store.Authorization()}
}

type applyErroringAuthorization struct {
	store.AuthorizationStore
}

func (a applyErroringAuthorization) Apply(context.Context, store.AuthorizationApplyCommand) (*store.AuthorizationSnapshot, error) {
	return nil, errors.New("authorization apply unavailable")
}

// TASK-365 regression: a failed Apply used to hand its nil snapshot back to
// loadPolicies, which then read durable.PolicyVersion in the error branch and
// panicked. The CI traceback put the fault at casbin.go:161 while the reloader
// ticked into a store that was being torn down.
func TestLoadPoliciesFailsClosedWhenApplyReturnsNilSnapshot(t *testing.T) {
	st, err := sqlitestore.Open(filepath.Join(t.TempDir(), "auth.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	// Seed an organization so the compiled projection differs from the empty
	// durable snapshot and loadPolicies really takes the Apply branch.
	seedAuthorization(t, st)

	e, err := NewEnforcer(applyErroringStore{Store: st}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(e.Close)

	var loadErr error
	require.NotPanics(t, func() { loadErr = e.LoadPolicies(context.Background()) },
		"a failed Apply must not turn the error branch into a nil dereference")
	require.Error(t, loadErr)
	assert.Contains(t, loadErr.Error(), "persist authorization rules")
	assert.False(t, e.PolicyHealthy(), "a failed refresh must fail closed")
}

// gatedStore blocks the first policy load until release is closed, so the test
// can hold a reload in flight while Close runs.
type gatedStore struct {
	store.Store
	auth *gatedAuthorization
}

func (g gatedStore) Authorization() store.AuthorizationStore { return g.auth }

type gatedAuthorization struct {
	store.AuthorizationStore
	armed   *atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedAuthorization) Load(ctx context.Context) (*store.AuthorizationSnapshot, error) {
	if g.armed.Load() {
		g.once.Do(func() {
			close(g.entered)
			<-g.release
		})
	}
	return g.AuthorizationStore.Load(ctx)
}

func TestPolicyReloaderCloseWaitsForInFlightReload(t *testing.T) {
	st, err := sqlitestore.Open(filepath.Join(t.TempDir(), "auth.db"))
	require.NoError(t, err)

	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	armed := new(atomic.Bool)
	gated := &gatedAuthorization{
		AuthorizationStore: st.Authorization(),
		armed:              armed,
		entered:            make(chan struct{}),
		release:            release,
	}
	e, err := NewEnforcer(gatedStore{Store: st, auth: gated}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	// NewSyncedEnforcer itself loads the policy through the adapter, so the gate
	// is armed only after construction; the first gated load is the reloader's.
	armed.Store(true)
	e.StartPolicyReloader(context.Background(), time.Millisecond)

	select {
	case <-gated.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the reloader never started a policy load")
	}

	closed := make(chan struct{})
	go func() {
		e.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a policy reload was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the in-flight reload finished")
	}
	select {
	case <-e.reloadDone:
	default:
		t.Fatal("Close returned before the reloader goroutine exited")
	}
}

// countingStore counts durable policy loads so the test can prove the reloader
// really stops rather than merely being unlikely to tick again.
type countingStore struct {
	store.Store
	loads *atomic.Int64
}

func (c countingStore) Authorization() store.AuthorizationStore {
	return &countingAuthorization{AuthorizationStore: c.Store.Authorization(), loads: c.loads}
}

type countingAuthorization struct {
	store.AuthorizationStore
	loads *atomic.Int64
}

func (c *countingAuthorization) Load(ctx context.Context) (*store.AuthorizationSnapshot, error) {
	c.loads.Add(1)
	return c.AuthorizationStore.Load(ctx)
}

func TestPolicyReloaderStopsAfterClose(t *testing.T) {
	st, err := sqlitestore.Open(filepath.Join(t.TempDir(), "auth.db"))
	require.NoError(t, err)

	loads := new(atomic.Int64)
	e, err := NewEnforcer(countingStore{Store: st, loads: loads}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	e.StartPolicyReloader(context.Background(), time.Millisecond)

	require.Eventually(t, func() bool { return loads.Load() >= 2 }, 5*time.Second, time.Millisecond,
		"the reloader must actually run before Close is tested")

	e.Close()
	settled := loads.Load()
	// Closing the store immediately after Close is the teardown order that
	// crashed CI: if Close left a reload running, the next tick would race it.
	require.NoError(t, st.Close())

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, settled, loads.Load(), "the reloader must not run after Close returns")

	assert.ErrorIs(t, e.LoadPolicies(context.Background()), ErrEnforcerClosed,
		"a closed enforcer must fail closed instead of touching the store")
	assert.Equal(t, settled, loads.Load())

	e.StartPolicyReloader(context.Background(), time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, settled, loads.Load(), "starting a reloader after Close must be a no-op")
	e.Close()
}
