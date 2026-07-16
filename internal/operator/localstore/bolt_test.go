package localstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoltStore_SaveAndGet(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := OpenBolt(path)
	require.NoError(t, err)
	defer store.Close()

	e := &CommandEntry{
		CommandID:   "cmd-1",
		OutboxID:    "out-1",
		OperationID: "op-1",
		Sequence:    1,
		Payload:     []byte(`{"type":"INSTALL"}`),
		Status:      StatusPending,
	}
	require.NoError(t, store.Save(ctx, e))

	got, err := store.Get(ctx, "cmd-1")
	require.NoError(t, err)
	assert.Equal(t, "cmd-1", got.CommandID)
	assert.Equal(t, "out-1", got.OutboxID)
	assert.Equal(t, int64(1), got.Sequence)
	assert.Equal(t, StatusPending, got.Status)
}

func TestBoltStore_GetByOutboxID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := OpenBolt(path)
	require.NoError(t, err)
	defer store.Close()

	e := &CommandEntry{
		CommandID:   "cmd-2",
		OutboxID:    "out-2",
		OperationID: "op-2",
		Sequence:    2,
		Payload:     []byte(`{}`),
		Status:      StatusPending,
	}
	require.NoError(t, store.Save(ctx, e))

	got, err := store.GetByOutboxID(ctx, "out-2")
	require.NoError(t, err)
	assert.Equal(t, "cmd-2", got.CommandID)
}

func TestBoltStore_UpdateStatus(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := OpenBolt(path)
	require.NoError(t, err)
	defer store.Close()

	e := &CommandEntry{
		CommandID: "cmd-3",
		OutboxID:  "out-3",
		Sequence:  3,
		Status:    StatusPending,
	}
	require.NoError(t, store.Save(ctx, e))

	require.NoError(t, store.UpdateStatus(ctx, "cmd-3", StatusSucceeded, `{"result":"ok"}`))

	got, err := store.Get(ctx, "cmd-3")
	require.NoError(t, err)
	assert.Equal(t, StatusSucceeded, got.Status)
	assert.Equal(t, `{"result":"ok"}`, got.ResultJSON)
}

func TestBoltStore_ListActive(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := OpenBolt(path)
	require.NoError(t, err)
	defer store.Close()

	// Create pending and terminal commands.
	for i, status := range []string{StatusPending, StatusRunning, StatusSucceeded, StatusFailed} {
		require.NoError(t, store.Save(ctx, &CommandEntry{
			CommandID: "cmd-" + string(rune('a'+i)),
			OutboxID:  "out-" + string(rune('a'+i)),
			Sequence:  int64(i + 1),
			Status:    status,
		}))
	}

	active, err := store.ListActive(ctx)
	require.NoError(t, err)
	assert.Len(t, active, 2) // pending + running
}

func TestBoltStore_LastSequence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := OpenBolt(path)
	require.NoError(t, err)
	defer store.Close()

	seq, err := store.LastSequence(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), seq)

	require.NoError(t, store.Save(ctx, &CommandEntry{
		CommandID: "cmd-s",
		Sequence:  42,
		Status:    StatusPending,
	}))

	seq, err = store.LastSequence(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(42), seq)
}

func TestBoltStore_Reopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	// First session: save a pending command.
	store1, err := OpenBolt(path)
	require.NoError(t, err)
	require.NoError(t, store1.Save(ctx, &CommandEntry{
		CommandID:   "cmd-r",
		OutboxID:    "out-r",
		Sequence:    10,
		Payload:     []byte(`{"type":"INSTALL"}`),
		Status:      StatusPending,
	}))
	require.NoError(t, store1.Close())

	// Simulate restart: reopen and verify the command is still there.
	store2, err := OpenBolt(path)
	require.NoError(t, err)
	defer store2.Close()

	got, err := store2.Get(ctx, "cmd-r")
	require.NoError(t, err)
	assert.Equal(t, "cmd-r", got.CommandID)
	assert.Equal(t, StatusPending, got.Status)

	active, err := store2.ListActive(ctx)
	require.NoError(t, err)
	assert.Len(t, active, 1)
	assert.Equal(t, "cmd-r", active[0].CommandID)

	// Mark as succeeded and verify it's no longer active.
	require.NoError(t, store2.UpdateStatus(ctx, "cmd-r", StatusSucceeded, `{"result":"ok"}`))
	active, err = store2.ListActive(ctx)
	require.NoError(t, err)
	assert.Empty(t, active)

	// Delete temp db for clean test.
	_ = os.Remove(path)
}
