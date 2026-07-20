package audit

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestEmitter_ShutdownReturnsSpoolFailed(t *testing.T) {
	st := &memoryStore{err: errors.New("database unavailable")}
	invalidParent := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(invalidParent, []byte("file"), 0o600))
	emitter := NewEmitter(st, slog.Default(), EmitterConfig{BufferSize: 1, BatchSize: 10, FlushInterval: time.Hour, SpoolPath: filepath.Join(invalidParent, "audit.jsonl")})
	require.True(t, emitter.Emit(testEvent(store.AuditActorService, "svc-1")).Accepted)
	err := emitter.Shutdown(t.Context())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSpoolFailed)
}

func TestSpoolRecoverer_CorruptLineRetainsFile(t *testing.T) {
	spoolPath := filepath.Join(t.TempDir(), "audit.jsonl")
	require.NoError(t, os.WriteFile(spoolPath, []byte("not-json\n"), 0o600))
	count, err := NewSpoolRecoverer(&memoryStore{}, slog.Default()).Recover(t.Context(), spoolPath)
	require.Error(t, err)
	assert.Zero(t, count)
	assert.FileExists(t, spoolPath)
}
