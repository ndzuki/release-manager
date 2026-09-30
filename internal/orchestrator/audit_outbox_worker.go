package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/store"
)

// AuditOutboxWorker drains the audit outbox into the audit query surface (TASK-231).
//
// REQ-068 has every state transition write the immutable decision record, the audit outbox row
// and the notification outbox row in one transaction. The notification side has had a drain
// since it was written; the audit side never did, so approvals were recorded in the outbox and
// never reached audit_events -- the platform promised a complete audit trail and only stored
// one. This is the missing drain.
//
// Consumption is at-least-once, and the sink is the DESENSITIZATION path: rows are emitted
// through audit.Sink (the same emitter everything else uses), never written into audit_events
// directly, because AGENTS.md hard constraint 6 forbids bypassing redaction.
type AuditOutboxWorker struct {
	outbox       store.AuditOutboxStore
	sink         audit.Sink
	logger       *slog.Logger
	pollInterval time.Duration
	batchSize    int
}

// AuditOutboxWorkerConfig tunes the poll loop.
type AuditOutboxWorkerConfig struct {
	PollInterval time.Duration
	BatchSize    int
}

// DefaultAuditOutboxWorkerConfig returns the production defaults.
func DefaultAuditOutboxWorkerConfig() AuditOutboxWorkerConfig {
	return AuditOutboxWorkerConfig{PollInterval: 30 * time.Second, BatchSize: 50}
}

// NewAuditOutboxWorker builds the worker. A nil sink disables delivery and is reported by Run
// rather than silently acknowledging entries.
func NewAuditOutboxWorker(
	outbox store.AuditOutboxStore,
	sink audit.Sink,
	logger *slog.Logger,
	cfg AuditOutboxWorkerConfig,
) *AuditOutboxWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultAuditOutboxWorkerConfig().PollInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultAuditOutboxWorkerConfig().BatchSize
	}
	return &AuditOutboxWorker{
		outbox: outbox, sink: sink, logger: logger,
		pollInterval: cfg.PollInterval, batchSize: cfg.BatchSize,
	}
}

// Run polls until the context is cancelled.
func (w *AuditOutboxWorker) Run(ctx context.Context) {
	if w.sink == nil {
		w.logger.Error("audit outbox worker has no sink; audit entries stay queued")
		return
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.DrainOnce(ctx); err != nil && ctx.Err() == nil {
			w.logger.Error("audit outbox drain failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// DrainOnce delivers one batch and returns how many entries were acknowledged.
//
// An entry the sink rejects is deliberately NOT acknowledged: the audit record would otherwise
// disappear, which is the failure this worker exists to fix. Such a row is re-listed on every
// pass and reported, and rows behind it still drain because a rejection only skips that entry.
func (w *AuditOutboxWorker) DrainOnce(ctx context.Context) (int, error) {
	if w.sink == nil {
		return 0, fmt.Errorf("audit outbox worker: nil sink")
	}
	entries, err := w.outbox.ListUndelivered(ctx, w.batchSize)
	if err != nil {
		return 0, fmt.Errorf("list undelivered audit outbox: %w", err)
	}
	delivered := 0
	for _, entry := range entries {
		result := w.sink.Emit(auditEventFromOutbox(entry))
		if !result.Accepted {
			w.logger.Warn("audit outbox entry rejected by the audit pipeline; keeping it queued",
				"outbox_id", entry.ID, "event_type", entry.EventType, "code", result.Code, "err", result.Err)
			continue
		}
		if err := w.outbox.MarkDelivered(ctx, entry.ID, time.Now().UTC()); err != nil {
			return delivered, fmt.Errorf("mark audit outbox delivered: %w", err)
		}
		delivered++
	}
	return delivered, nil
}

// auditEventFromOutbox maps an outbox row onto the audit event the emitter accepts.
//
// The payload is written by the transition that owns the row and is already desensitized (an
// approval comment is stored as a hash, the rejection reason is removed), so it travels as the
// event's metadata. Unknown payload shapes still produce an event: an audit record whose shape
// we do not recognize must not vanish.
func auditEventFromOutbox(entry *store.ApprovalOutboxEntry) *store.AuditEvent {
	event := &store.AuditEvent{
		ID:           entry.ID,
		ActorKind:    store.AuditActorSystem,
		ResourceType: "audit_outbox",
		ResourceID:   entry.ID,
		Action:       entry.EventType,
		Status:       "succeeded",
		CreatedAt:    entry.CreatedAt,
		Metadata:     map[string]string{"outbox_event_type": entry.EventType},
	}
	var payload map[string]any
	if err := json.Unmarshal(entry.PayloadJSON, &payload); err != nil || payload == nil {
		event.Metadata["payload_unparsed"] = "true"
		return event
	}
	event.ID = stringField(payload, "event_id", entry.ID)
	event.OrganizationID = stringField(payload, "organization_id", "")
	event.Role = stringField(payload, "actor_role", "")
	if actor := stringField(payload, "actor_user_id", ""); actor != "" {
		event.ActorKind = store.AuditActorUser
		event.ActorID = actor
	}
	switch {
	case stringField(payload, "revision_id", "") != "":
		event.ResourceType = "values_revision"
		event.ResourceID = stringField(payload, "revision_id", "")
	case stringField(payload, "bundle_id", "") != "":
		event.ResourceType = "release_bundle"
		event.ResourceID = stringField(payload, "bundle_id", "")
	}
	if status := stringField(payload, "state", stringField(payload, "status", "")); status != "" {
		event.Status = status
	}
	if occurred := stringField(payload, "occurred_at", ""); occurred != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, occurred); err == nil {
			event.CreatedAt = parsed
		}
	}
	if definition := stringField(payload, "release_definition_id", ""); definition != "" {
		event.Metadata["release_definition_id"] = definition
	}
	if requestID := stringField(payload, "request_id", ""); requestID != "" {
		event.Metadata["request_id"] = requestID
	}
	return event
}

func stringField(payload map[string]any, key, fallback string) string {
	value, ok := payload[key].(string)
	if !ok || value == "" {
		return fallback
	}
	return value
}
