// Package audit provides asynchronous audit event collection and persistence.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// Emitter buffers and asynchronously persists audit events.
type Emitter struct {
	store         store.AuditEventStore
	logger        *slog.Logger
	buffer        chan *store.AuditEvent
	batchSize     int
	flushInterval time.Duration
	spoolPath     string
	metrics       metrics

	mu           sync.Mutex
	closed       bool
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
	workerWG     sync.WaitGroup
}

// EmitterConfig configures the audit emitter.
type EmitterConfig struct {
	BufferSize    int
	FlushInterval time.Duration
	BatchSize     int
	SpoolPath     string
}

// DefaultConfig returns production defaults.
func DefaultConfig() EmitterConfig {
	return EmitterConfig{BufferSize: 4096, FlushInterval: 5 * time.Second, BatchSize: 200, SpoolPath: "data/audit_spool.jsonl"}
}

// NewEmitter creates an emitter and starts its single persistence worker.
func NewEmitter(st store.AuditEventStore, logger *slog.Logger, cfg EmitterConfig) *Emitter {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 1
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	e := &Emitter{store: st, logger: logger, buffer: make(chan *store.AuditEvent, cfg.BufferSize), batchSize: cfg.BatchSize, flushInterval: cfg.FlushInterval, spoolPath: cfg.SpoolPath, shutdownDone: make(chan struct{})}
	e.workerWG.Add(1)
	go e.worker()
	return e
}

// Emit validates and queues an event without blocking the caller.
func (e *Emitter) Emit(event *store.AuditEvent) Result {
	e.metrics.received.Add(1)
	normalized, err := Normalize(event)
	if err != nil {
		e.metrics.rejected.Add(1)
		id := ""
		if event != nil {
			id = event.ID
		}
		return Result{EventID: id, Code: ErrorInvalidEvent, Err: &EventError{Code: ErrorInvalidEvent, ID: id, Err: err}}
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		e.metrics.rejected.Add(1)
		return Result{EventID: normalized.ID, Code: ErrorStoreUnavailable, Err: &EventError{Code: ErrorStoreUnavailable, ID: normalized.ID, Err: ErrEmitterClosed}}
	}
	select {
	case e.buffer <- normalized:
		e.mu.Unlock()
		e.metrics.accepted.Add(1)
		return Result{EventID: normalized.ID, Accepted: true}
	default:
		e.mu.Unlock()
		e.metrics.rejected.Add(1)
		e.metrics.bufferFull.Add(1)
		e.logger.Warn("audit buffer full", "event_id", normalized.ID, "resource_type", normalized.ResourceType, "action", normalized.Action)
		return Result{EventID: normalized.ID, Code: ErrorBufferFull, Err: &EventError{Code: ErrorBufferFull, ID: normalized.ID, Err: ErrBufferFull}}
	}
}

// Metrics returns a race-safe counter snapshot.
func (e *Emitter) Metrics() MetricsSnapshot { return e.metrics.snapshot() }

// Shutdown is idempotent and waits for the worker to finish draining.
func (e *Emitter) Shutdown(ctx context.Context) error {
	e.shutdownOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		close(e.buffer)
		e.mu.Unlock()
		go func() {
			e.workerWG.Wait()
			close(e.shutdownDone)
		}()
	})
	select {
	case <-e.shutdownDone:
		return e.shutdownErr
	case <-ctx.Done():
		return fmt.Errorf("audit emitter shutdown: %w", ctx.Err())
	}
}

func (e *Emitter) worker() {
	defer e.workerWG.Done()
	ticker := time.NewTicker(e.flushInterval)
	defer ticker.Stop()
	batch := make([]*store.AuditEvent, 0, e.batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		pending := batch
		batch = make([]*store.AuditEvent, 0, e.batchSize)
		if err := e.persist(context.Background(), pending); err != nil {
			e.logger.Error("audit batch persistence failed", "count", len(pending), "error", err)
			batch = append(pending, batch...)
		}
	}
	for {
		select {
		case event, ok := <-e.buffer:
			if !ok {
				flush()
				if len(batch) > 0 {
					e.shutdownErr = e.spool(batch)
				}
				return
			}
			batch = append(batch, event)
			if len(batch) >= e.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (e *Emitter) persist(ctx context.Context, events []*store.AuditEvent) error {
	if err := e.store.CreateBatch(ctx, events); err != nil {
		e.metrics.storeFailure.Add(1)
		return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	e.metrics.persisted.Add(uint64(len(events)))
	return nil
}

func (e *Emitter) spool(events []*store.AuditEvent) error {
	if len(events) == 0 {
		return nil
	}
	if dir := filepath.Dir(e.spoolPath); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			e.metrics.storeFailure.Add(1)
			return fmt.Errorf("%w: create spool directory: %w", ErrSpoolFailed, err)
		}
	}
	f, err := os.OpenFile(e.spoolPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: open spool file: %w", ErrSpoolFailed, err)
	}
	encoder := json.NewEncoder(f)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			_ = f.Close()
			return fmt.Errorf("%w: encode event %s: %w", ErrSpoolFailed, event.ID, err)
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: sync spool file: %w", ErrSpoolFailed, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("%w: close spool file: %w", ErrSpoolFailed, err)
	}
	e.metrics.spooled.Add(uint64(len(events)))
	return nil
}

func (e *Emitter) SpoolPath() string { return e.spoolPath }
