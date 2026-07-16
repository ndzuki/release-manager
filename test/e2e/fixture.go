package e2e

import "sync"

// Fixture holds snapshot data shared across E2E stages.
//
// Each stage may read the fixture but MUST NOT mutate shared keys directly.
// A stage that needs to pass data forward should create a new Fixture via
// Clone, add its keys, and return it as its output.
type Fixture struct {
	mu   sync.RWMutex
	data map[string]any
}

// NewFixture creates an empty Fixture.
func NewFixture() *Fixture {
	return &Fixture{data: make(map[string]any)}
}

// Get retrieves a value by key. Returns nil if not found.
func (f *Fixture) Get(key string) any {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.data[key]
}

// Set stores a value under key.
func (f *Fixture) Set(key string, val any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = val
}

// Has reports whether key exists.
func (f *Fixture) Has(key string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, ok := f.data[key]
	return ok
}

// Clone returns a shallow copy. The underlying values are not deep-copied.
func (f *Fixture) Clone() *Fixture {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cp := make(map[string]any, len(f.data))
	for k, v := range f.data {
		cp[k] = v
	}
	return &Fixture{data: cp}
}
