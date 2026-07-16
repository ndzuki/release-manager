// Package audit provides asynchronous audit event collection and persistence.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
)

// Emitter buffers and asynchronously persists audit events.
// It implements graceful shutdown with spool-to-file fallback.
type Emitter struct {
	store      store.AuditEventStore
	logger     *slog.Logger
	buffer     chan *store.AuditEvent
	bufferSize int

	// Prometheus-compatible counters (simple int64 gauges).
	// These can be exposed via expvar or a /metrics endpoint.
	EventsReceived  int64
	EventsPersisted int64
	EventsDropped   int64
	BufferFullCount int64

	spoolPath string

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// EmitterConfig configures the AuditEmitter.
type EmitterConfig struct {
	// BufferSize is the max number of pending events before the channel blocks.
	BufferSize int

	// FlushInterval is how often the emitter flushes buffered events to SQLite.
	FlushInterval time.Duration

	// BatchSize is the max number of events per SQLite insert batch.
	BatchSize int

	// SpoolPath is the path to the spool file for graceful shutdown drain.
	SpoolPath string
}

// DefaultConfig returns a production-sane default configuration.
func DefaultConfig() EmitterConfig {
	return EmitterConfig{
		BufferSize:    4096,
		FlushInterval: 5 * time.Second,
		BatchSize:     200,
		SpoolPath:     "data/audit_spool.jsonl",
	}
}

// NewEmitter creates an AuditEmitter and starts the background flush goroutine.
func NewEmitter(st store.AuditEventStore, logger *slog.Logger, cfg EmitterConfig) *Emitter {
	e := &Emitter{
		store:      st,
		logger:     logger,
		buffer:     make(chan *store.AuditEvent, cfg.BufferSize),
		bufferSize: cfg.BufferSize,
		spoolPath:  cfg.SpoolPath,
	}
	e.wg.Add(1)
	go e.flushLoop(cfg.FlushInterval, cfg.BatchSize)
	return e
}

// Emit enqueues an audit event for asynchronous persistence.
// Returns false if the buffer is full (event dropped).
// AC-050-03: buffer full increments BufferFullCount counter.
func (e *Emitter) Emit(event *store.AuditEvent) bool {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return false
	}
	e.mu.Unlock()

	if event.ID == "" {
		event.ID = uuid.New().String()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if event.Metadata == nil {
		event.Metadata = make(map[string]string)
	}

	select {
	case e.buffer <- event:
		e.EventsReceived++
		return true
	default:
		e.BufferFullCount++
		e.EventsDropped++
		e.logger.Warn("audit buffer full, event dropped",
			"event_id", event.ID,
			"resource_type", event.ResourceType,
			"action", event.Action,
		)
		return false
	}
}

// Shutdown gracefully drains the buffer.
// First attempts to persist remaining events to SQLite.
// On failure, spools events to a JSONL file for later recovery.
// Returns when all events are drained or context is cancelled.
func (e *Emitter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()

	close(e.buffer)

	done := make(chan struct{})
	var shutdownErr error
	go func() {
		defer close(done)
		shutdownErr = e.drainRemaining(ctx)
		e.wg.Wait()
	}()

	select {
	case <-done:
		return shutdownErr
	case <-ctx.Done():
		return fmt.Errorf("audit emitter shutdown timed out: %w", ctx.Err())
	}
}

func (e *Emitter) flushLoop(interval time.Duration, batchSize int) {
	defer e.wg.Done()

	var batch []*store.AuditEvent
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-e.buffer:
			if !ok {
				// Channel closed; flush remaining.
				if len(batch) > 0 {
					e.flushBatch(batch)
				}
				return
			}
			batch = append(batch, event)
			if len(batch) >= batchSize {
				e.flushBatch(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				e.flushBatch(batch)
				batch = batch[:0]
			}
		}
	}
}

func (e *Emitter) flushBatch(batch []*store.AuditEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.store.CreateBatch(ctx, batch); err != nil {
		e.logger.Error("failed to persist audit batch",
			"count", len(batch),
			"error", err,
		)
		return
	}
	e.EventsPersisted += int64(len(batch))
}

// drainRemaining collects any remaining events from the closed channel and
// attempts to persist them. On failure, spools to file.
func (e *Emitter) drainRemaining(ctx context.Context) error {
	var remaining []*store.AuditEvent
	for event := range e.buffer {
		remaining = append(remaining, event)
	}

	if len(remaining) == 0 {
		return nil
	}

	persistCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := e.store.CreateBatch(persistCtx, remaining); err != nil {
		e.logger.Error("failed to persist remaining audit events during shutdown, spooling to file",
			"count", len(remaining),
			"path", e.spoolPath,
			"error", err,
		)
		if spoolErr := e.spoolToFile(remaining); spoolErr != nil {
			return fmt.Errorf("persist failed (%w) and spool failed (%w)", err, spoolErr)
		}
		return fmt.Errorf("persist failed, events spooled to %s: %w", e.spoolPath, err)
	}
	e.EventsPersisted += int64(len(remaining))
	return nil
}

// spoolToFile writes audit events to a JSONL file for later recovery (AC-050-04).
func (e *Emitter) spoolToFile(events []*store.AuditEvent) error {
	f, err := os.OpenFile(e.spoolPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open spool file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return fmt.Errorf("spool encode event %s: %w", ev.ID, err)
		}
	}
	return nil
}
