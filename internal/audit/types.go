package audit

import (
	"errors"

	"github.com/ndzuki/release-manager/internal/store"
)

// ErrorCode is the stable machine-readable audit failure code.
type ErrorCode string

const (
	ErrorInvalidEvent     ErrorCode = "invalid_event"
	ErrorBufferFull       ErrorCode = "buffer_full"
	ErrorStoreUnavailable ErrorCode = "store_unavailable"
	ErrorSpoolFailed      ErrorCode = "spool_failed"
)

var (
	// ErrInvalidEvent reports an event rejected before entering the buffer.
	ErrInvalidEvent = errors.New("invalid audit event")
	// ErrBufferFull reports that the bounded audit buffer rejected an event.
	ErrBufferFull = errors.New("audit buffer full")
	// ErrStoreUnavailable reports a persistence failure.
	ErrStoreUnavailable = errors.New("audit store unavailable")
	// ErrSpoolFailed reports a durable spool write failure.
	ErrSpoolFailed = errors.New("audit spool failed")
	// ErrEmitterClosed reports an emit attempt after shutdown started.
	ErrEmitterClosed = errors.New("audit emitter closed")
)

// Result describes whether an event was accepted for asynchronous persistence.
type Result struct {
	EventID  string
	Accepted bool
	Code     ErrorCode
	Err      error
}

// Sink receives normalized audit events.
type Sink interface {
	Emit(*store.AuditEvent) Result
}

// MetricsSnapshot is a race-safe point-in-time view of emitter counters.
type MetricsSnapshot struct {
	Received     uint64
	Accepted     uint64
	Persisted    uint64
	Rejected     uint64
	BufferFull   uint64
	StoreFailure uint64
	Spooled      uint64
}
