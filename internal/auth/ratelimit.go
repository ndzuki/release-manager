package auth

import (
	"sync"
	"time"
)

// RateLimiter implements a simple sliding-window rate limiter per key.
type RateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	max      int
	window   time.Duration
}

// NewRateLimiter creates a new rate limiter.
// maxAttempts is the maximum number of attempts allowed within the window.
func NewRateLimiter(maxAttempts int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		attempts: make(map[string][]time.Time),
		max:      maxAttempts,
		window:   window,
	}
}

// Allow returns true if the key is allowed to proceed.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	cutoff := now.Add(-r.window)

	entries := r.attempts[key]
	// Trim expired entries.
	var valid []time.Time
	for _, t := range entries {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= r.max {
		r.attempts[key] = valid
		return false
	}

	valid = append(valid, now)
	r.attempts[key] = valid
	return true
}

// Cleanup removes expired entries. Call periodically to prevent memory leaks.
func (r *RateLimiter) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	cutoff := now.Add(-r.window)
	for key, entries := range r.attempts {
		var valid []time.Time
		for _, t := range entries {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(r.attempts, key)
		} else {
			r.attempts[key] = valid
		}
	}
}
