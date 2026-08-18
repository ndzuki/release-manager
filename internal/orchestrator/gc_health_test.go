package orchestrator

import (
	"sync"
	"testing"
	"time"
)

type gcHealthTestClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *gcHealthTestClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *gcHealthTestClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func TestGCHealth_StartupGraceAndDegraded(t *testing.T) {
	started := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))
	clock := &gcHealthTestClock{now: started}
	health := NewGCHealth(time.Hour, clock)

	snapshot := health.Snapshot()
	if snapshot.Status != GCHealthHealthy || !snapshot.Healthy {
		t.Fatalf("startup status = %q, healthy=%t; want healthy", snapshot.Status, snapshot.Healthy)
	}
	if !snapshot.StartedAt.Equal(started.UTC()) || !snapshot.LastSuccess.Equal(started.UTC()) {
		t.Fatalf("startup timestamps = started %s, last success %s; want UTC %s", snapshot.StartedAt, snapshot.LastSuccess, started.UTC())
	}

	clock.Advance(2*time.Hour - time.Nanosecond)
	if got := health.Snapshot().Status; got != GCHealthHealthy {
		t.Fatalf("status during grace = %q; want healthy", got)
	}

	clock.Advance(time.Nanosecond)
	snapshot = health.Snapshot()
	if snapshot.Status != GCHealthDegraded || snapshot.Healthy {
		t.Fatalf("status at 2x interval = %q, healthy=%t; want degraded", snapshot.Status, snapshot.Healthy)
	}
	if health.Health() {
		t.Fatal("Health() = true after 2x interval without success; want false")
	}
}

func TestGCHealth_RecordAttemptAndSuccess(t *testing.T) {
	started := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	clock := &gcHealthTestClock{now: started}
	health := NewGCHealth(time.Minute, clock)

	clock.Advance(10 * time.Second)
	health.RecordAttempt()
	clock.Advance(5 * time.Second)
	health.RecordSuccess()

	snapshot := health.Snapshot()
	if snapshot.Attempts != 1 || snapshot.Successes != 1 {
		t.Fatalf("counts = attempts %d, successes %d; want 1, 1", snapshot.Attempts, snapshot.Successes)
	}
	if !snapshot.LastAttempt.Equal(started.Add(10 * time.Second)) {
		t.Fatalf("last attempt = %s; want %s", snapshot.LastAttempt, started.Add(10*time.Second))
	}
	if !snapshot.LastSuccess.Equal(started.Add(15 * time.Second)) {
		t.Fatalf("last success = %s; want %s", snapshot.LastSuccess, started.Add(15*time.Second))
	}
	if !health.Health() {
		t.Fatal("Health() = false after recent success; want true")
	}
}

func TestGCHealth_Disabled(t *testing.T) {
	started := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	clock := &gcHealthTestClock{now: started}
	health := NewGCHealth(0, clock)
	clock.Advance(24 * time.Hour)

	snapshot := health.Snapshot()
	if snapshot.Status != GCHealthDisabled || !snapshot.Disabled {
		t.Fatalf("disabled snapshot = status %q, disabled=%t; want disabled", snapshot.Status, snapshot.Disabled)
	}
	if !snapshot.Healthy || !health.Health() {
		t.Fatal("disabled GC should not make service unhealthy")
	}
}

func TestGCHealth_ConcurrentUpdates(t *testing.T) {
	clock := &gcHealthTestClock{now: time.Now().UTC()}
	health := NewGCHealth(time.Hour, clock)
	const workers = 8
	const updates = 100

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range updates {
				health.RecordAttempt()
				health.RecordSuccess()
				_ = health.Snapshot()
			}
		})
	}
	wg.Wait()

	snapshot := health.Snapshot()
	want := uint64(workers * updates)
	if snapshot.Attempts != want || snapshot.Successes != want {
		t.Fatalf("counts = attempts %d, successes %d; want %d, %d", snapshot.Attempts, snapshot.Successes, want, want)
	}
}
