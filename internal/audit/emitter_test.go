package audit

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

type memoryStore struct {
	mu     sync.Mutex
	events []*store.AuditEvent
	err    error
	block  <-chan struct{}
}

func (s *memoryStore) Create(ctx context.Context, event *store.AuditEvent) error {
	return s.CreateBatch(ctx, []*store.AuditEvent{event})
}

func (s *memoryStore) CreateBatch(ctx context.Context, events []*store.AuditEvent) error {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
	return nil
}

func (s *memoryStore) Query(_ context.Context, _ store.AuditEventFilter, _ string, _ int) (*store.AuditEventPage, error) {
	return &store.AuditEventPage{}, nil
}

func (s *memoryStore) GetByID(_ context.Context, _ string) (*store.AuditEvent, error) {
	return nil, store.ErrNotFound
}

func (s *memoryStore) Count(_ context.Context, _ store.AuditEventFilter) (int64, error) {
	return 0, nil
}

func (s *memoryStore) ListByResource(_ context.Context, _, _ string) ([]*store.AuditEvent, error) {
	return nil, nil
}

func (s *memoryStore) ListOlderThan(_ context.Context, _ time.Time, _ int) ([]*store.AuditEvent, error) {
	return nil, nil
}

func (s *memoryStore) DeleteByIDs(_ context.Context, _ []string) (int64, error) {
	return 0, nil
}

func testEvent(kind store.AuditActorKind, id string) *store.AuditEvent {
	return &store.AuditEvent{
		ActorKind:    kind,
		ActorID:      id,
		ResourceType: "release",
		ResourceID:   "rel-1",
		Action:       "write",
		Status:       "accepted",
		Metadata:     map[string]string{"token": "secret-token", "safe": "value"},
	}
}

func TestEmitter_AcceptsActorAndSanitizes(t *testing.T) {
	st := &memoryStore{}
	emitter := NewEmitter(st, slog.New(slog.NewTextHandler(os.Stderr, nil)), EmitterConfig{BufferSize: 2, BatchSize: 1, FlushInterval: time.Hour, SpoolPath: filepath.Join(t.TempDir(), "audit.jsonl")})
	for _, kind := range []store.AuditActorKind{store.AuditActorAPIKey, store.AuditActorService} {
		result := emitter.Emit(testEvent(kind, string(kind)+"-1"))
		require.True(t, result.Accepted)
		require.NotEmpty(t, result.EventID)
	}
	require.NoError(t, emitter.Shutdown(t.Context()))
	require.Len(t, st.events, 2)
	for _, event := range st.events {
		assert.NotEmpty(t, event.ActorID)
		assert.Equal(t, "****REDACTED****", event.Metadata["token"])
		assert.Equal(t, "value", event.Metadata["safe"])
	}
}

func TestEmitter_RejectsInvalidAndFullBuffer(t *testing.T) {
	blocked := make(chan struct{})
	st := &memoryStore{block: blocked}
	emitter := NewEmitter(st, slog.Default(), EmitterConfig{BufferSize: 1, BatchSize: 1, FlushInterval: time.Hour, SpoolPath: filepath.Join(t.TempDir(), "audit.jsonl")})
	invalid := emitter.Emit(testEvent(store.AuditActorAPIKey, ""))
	assert.False(t, invalid.Accepted)
	assert.Equal(t, ErrorInvalidEvent, invalid.Code)

	require.True(t, emitter.Emit(testEvent(store.AuditActorService, "svc-1")).Accepted)
	require.Eventually(t, func() bool { return len(emitter.buffer) == 0 }, time.Second, time.Millisecond)
	require.True(t, emitter.Emit(testEvent(store.AuditActorService, "svc-2")).Accepted)
	full := emitter.Emit(testEvent(store.AuditActorService, "svc-3"))
	assert.False(t, full.Accepted)
	assert.Equal(t, ErrorBufferFull, full.Code)
	assert.Equal(t, uint64(1), emitter.Metrics().BufferFull)
	close(blocked)
	require.NoError(t, emitter.Shutdown(t.Context()))
}

func TestEmitter_ShutdownSpoolsFailedBatch(t *testing.T) {
	spoolPath := filepath.Join(t.TempDir(), "spool", "audit.jsonl")
	st := &memoryStore{err: errors.New("database unavailable")}
	emitter := NewEmitter(st, slog.Default(), EmitterConfig{BufferSize: 2, BatchSize: 10, FlushInterval: time.Hour, SpoolPath: spoolPath})
	require.True(t, emitter.Emit(testEvent(store.AuditActorService, "svc-1")).Accepted)
	require.NoError(t, emitter.Shutdown(t.Context()))
	info, err := os.Stat(spoolPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Equal(t, uint64(1), emitter.Metrics().Spooled)

	recoveredStore := &memoryStore{}
	count, err := NewSpoolRecoverer(recoveredStore, slog.Default()).Recover(t.Context(), spoolPath)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.NoFileExists(t, spoolPath)
	assert.Len(t, recoveredStore.events, 1)
}

func TestEmitter_ShutdownIsIdempotent(t *testing.T) {
	emitter := NewEmitter(&memoryStore{}, slog.Default(), EmitterConfig{BufferSize: 1, BatchSize: 1, FlushInterval: time.Hour, SpoolPath: filepath.Join(t.TempDir(), "audit.jsonl")})
	require.True(t, emitter.Emit(testEvent(store.AuditActorAnonymous, "")).Accepted)
	require.NoError(t, emitter.Shutdown(t.Context()))
	require.NoError(t, emitter.Shutdown(t.Context()))
	result := emitter.Emit(testEvent(store.AuditActorService, "svc"))
	assert.False(t, result.Accepted)
	assert.ErrorIs(t, result.Err, ErrEmitterClosed)
}
