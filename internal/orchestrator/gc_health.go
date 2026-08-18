package orchestrator

import (
	"sync"
	"time"
)

// GCHealthStatus describes the current health of the cleanup garbage collector.
type GCHealthStatus string

const (
	GCHealthHealthy  GCHealthStatus = "healthy"
	GCHealthDegraded GCHealthStatus = "degraded"
	GCHealthDisabled GCHealthStatus = "disabled"
)

// GCHealthSnapshot is an immutable point-in-time view of GC health.
type GCHealthSnapshot struct {
	Status      GCHealthStatus `json:"status"`
	Healthy     bool           `json:"healthy"`
	Disabled    bool           `json:"disabled"`
	Interval    time.Duration  `json:"interval"`
	StartedAt   time.Time      `json:"started_at"`
	LastAttempt time.Time      `json:"last_attempt"`
	LastSuccess time.Time      `json:"last_success"`
	Attempts    uint64         `json:"attempts"`
	Successes   uint64         `json:"successes"`
}

// GCHealth tracks cleanup attempts and successes. It is safe for concurrent use.
type GCHealth struct {
	mu sync.RWMutex

	interval time.Duration
	now      func() time.Time

	startedAt   time.Time
	lastAttempt time.Time
	lastSuccess time.Time
	attempts    uint64
	successes   uint64
}

// NewGCHealth creates a GC health tracker. The optional time source is useful
// for deterministic tests; it may be either func() time.Time or an object with
// a Now() time.Time method. When omitted, UTC wall-clock time is used.
func NewGCHealth(interval time.Duration, sources ...any) *GCHealth {
	now := func() time.Time { return time.Now().UTC() }
	if len(sources) > 0 && sources[0] != nil {
		switch source := sources[0].(type) {
		case func() time.Time:
			now = source
		case interface{ Now() time.Time }:
			now = source.Now
		}
	}
	startedAt := now().UTC()
	return &GCHealth{
		interval:    interval,
		now:         now,
		startedAt:   startedAt,
		lastSuccess: startedAt,
	}
}

// RecordAttempt records one cleanup attempt at the current time.
func (h *GCHealth) RecordAttempt() {
	if h == nil {
		return
	}
	now := h.currentTime()
	h.mu.Lock()
	h.attempts++
	h.lastAttempt = now
	h.mu.Unlock()
}

// RecordSuccess records one successful cleanup at the current time.
func (h *GCHealth) RecordSuccess() {
	if h == nil {
		return
	}
	now := h.currentTime()
	h.mu.Lock()
	h.successes++
	h.lastSuccess = now
	h.mu.Unlock()
}

// Snapshot returns a race-free point-in-time health view.
func (h *GCHealth) Snapshot() GCHealthSnapshot {
	if h == nil {
		return GCHealthSnapshot{Status: GCHealthDisabled, Disabled: true}
	}
	now := h.currentTime()
	h.mu.RLock()
	defer h.mu.RUnlock()

	snapshot := GCHealthSnapshot{
		Interval:    h.interval,
		StartedAt:   h.startedAt,
		LastAttempt: h.lastAttempt,
		LastSuccess: h.lastSuccess,
		Attempts:    h.attempts,
		Successes:   h.successes,
	}
	if h.interval <= 0 {
		snapshot.Status = GCHealthDisabled
		snapshot.Disabled = true
		snapshot.Healthy = true
		return snapshot
	}
	if now.Sub(h.lastSuccess) >= 2*h.interval {
		snapshot.Status = GCHealthDegraded
		return snapshot
	}
	snapshot.Status = GCHealthHealthy
	snapshot.Healthy = true
	return snapshot
}

// Health reports whether cleanup GC is healthy. A disabled GC is considered
// healthy because no liveness deadline applies when its interval is zero.
func (h *GCHealth) Health() bool {
	return h.Snapshot().Healthy
}

func (h *GCHealth) currentTime() time.Time {
	now := h.now()
	return now.UTC()
}
