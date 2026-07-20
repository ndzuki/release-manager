package operator

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

type sessionStoreStub struct {
	statuses map[string]store.SessionStatus
}

func (s *sessionStoreStub) Create(context.Context, *store.Session) error        { return nil }
func (s *sessionStoreStub) Establish(context.Context, *store.Session) error     { return nil }
func (s *sessionStoreStub) Get(context.Context, string) (*store.Session, error) { return nil, nil }
func (s *sessionStoreStub) Heartbeat(context.Context, string) error             { return nil }
func (s *sessionStoreStub) UpdateStatus(_ context.Context, id string, status store.SessionStatus) error {
	s.statuses[id] = status
	return nil
}
func (s *sessionStoreStub) GetActiveByOperator(context.Context, string) (*store.Session, error) {
	return nil, store.ErrNotFound
}
func (s *sessionStoreStub) ListExpiredSuspect(context.Context, time.Duration) ([]*store.Session, error) {
	return nil, nil
}

func TestSessionRegistryTransitions(t *testing.T) {
	stub := &sessionStoreStub{statuses: map[string]store.SessionStatus{}}
	registry := NewSessionRegistry(stub, time.Second, 2*time.Second, slog.New(slog.DiscardHandler))
	at := time.Unix(100, 0)
	registry.Register("session-1", at)

	registry.evaluate(context.Background(), at.Add(1500*time.Millisecond))
	assert.Equal(t, store.SessionSuspect, stub.statuses["session-1"])

	registry.evaluate(context.Background(), at.Add(2500*time.Millisecond))
	assert.Equal(t, store.SessionOffline, stub.statuses["session-1"])

	registry.mu.RLock()
	_, ok := registry.entries["session-1"]
	registry.mu.RUnlock()
	require.False(t, ok)
}

func TestSessionRegistryHeartbeatPreventsTransition(t *testing.T) {
	stub := &sessionStoreStub{statuses: map[string]store.SessionStatus{}}
	registry := NewSessionRegistry(stub, time.Second, 2*time.Second, slog.New(slog.DiscardHandler))
	at := time.Unix(100, 0)
	registry.Register("session-1", at)
	registry.Heartbeat("session-1", at.Add(1500*time.Millisecond))

	registry.evaluate(context.Background(), at.Add(2*time.Second))
	assert.Empty(t, stub.statuses)
}
