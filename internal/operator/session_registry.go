package operator

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type registryEntry struct {
	sessionID     string
	lastHeartbeat time.Time
}

type SessionRegistry struct {
	mu           sync.RWMutex
	entries      map[string]registryEntry
	sessions     store.SessionStore
	suspectAfter time.Duration
	offlineAfter time.Duration
	checkEvery   time.Duration
	logger       *slog.Logger
}

func NewSessionRegistry(
	sessions store.SessionStore,
	suspectAfter time.Duration,
	offlineAfter time.Duration,
	logger *slog.Logger,
) *SessionRegistry {
	checkEvery := suspectAfter / 2
	if checkEvery <= 0 {
		checkEvery = time.Second
	}
	return &SessionRegistry{
		entries:      map[string]registryEntry{},
		sessions:     sessions,
		suspectAfter: suspectAfter,
		offlineAfter: offlineAfter,
		checkEvery:   checkEvery,
		logger:       logger,
	}
}

func (r *SessionRegistry) Register(sessionID string, lastHeartbeat time.Time) {
	r.mu.Lock()
	r.entries[sessionID] = registryEntry{
		sessionID:     sessionID,
		lastHeartbeat: lastHeartbeat,
	}
	r.mu.Unlock()
}

func (r *SessionRegistry) Heartbeat(sessionID string, at time.Time) {
	r.mu.Lock()
	entry, ok := r.entries[sessionID]
	if ok {
		entry.lastHeartbeat = at
		r.entries[sessionID] = entry
	}
	r.mu.Unlock()
}

func (r *SessionRegistry) Unregister(sessionID string) {
	r.mu.Lock()
	delete(r.entries, sessionID)
	r.mu.Unlock()
}

func (r *SessionRegistry) Run(ctx context.Context) {
	ticker := time.NewTicker(r.checkEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.evaluate(ctx, now.UTC())
		}
	}
}

func (r *SessionRegistry) evaluate(ctx context.Context, now time.Time) {
	type transition struct {
		sessionID string
		status    store.SessionStatus
	}

	transitions := []transition{}
	r.mu.RLock()
	for _, entry := range r.entries {
		age := now.Sub(entry.lastHeartbeat)
		switch {
		case age >= r.offlineAfter:
			transitions = append(transitions, transition{sessionID: entry.sessionID, status: store.SessionOffline})
		case age >= r.suspectAfter:
			transitions = append(transitions, transition{sessionID: entry.sessionID, status: store.SessionSuspect})
		}
	}
	r.mu.RUnlock()

	for _, transition := range transitions {
		if err := r.sessions.UpdateStatus(ctx, transition.sessionID, transition.status); err != nil {
			r.logger.Warn(
				"session state transition failed",
				"session_id", transition.sessionID,
				"status", transition.status,
				"error", err,
			)
			continue
		}
		if transition.status == store.SessionOffline {
			r.Unregister(transition.sessionID)
		}
	}
}
