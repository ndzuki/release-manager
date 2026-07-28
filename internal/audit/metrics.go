package audit

import "sync/atomic"

// metrics stores emitter counters without exposing mutable shared state.
type metrics struct {
	received     atomic.Uint64
	accepted     atomic.Uint64
	persisted    atomic.Uint64
	rejected     atomic.Uint64
	bufferFull   atomic.Uint64
	storeFailure atomic.Uint64
	spooled      atomic.Uint64
}

func (m *metrics) snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		Received:     m.received.Load(),
		Accepted:     m.accepted.Load(),
		Persisted:    m.persisted.Load(),
		Rejected:     m.rejected.Load(),
		BufferFull:   m.bufferFull.Load(),
		StoreFailure: m.storeFailure.Load(),
		Spooled:      m.spooled.Load(),
	}
}
