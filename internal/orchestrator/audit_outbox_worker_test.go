package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/store"
)

// rejectingAuditSink records what it accepted and rejects the events named in reject.
type rejectingAuditSink struct {
	events  []*store.AuditEvent
	reject  map[string]bool
	attempt int
}

func (s *rejectingAuditSink) Emit(event *store.AuditEvent) audit.Result {
	s.attempt++
	if s.reject[event.ID] {
		return audit.Result{EventID: event.ID, Accepted: false, Code: audit.ErrorCode("redaction_failed")}
	}
	s.events = append(s.events, event)
	return audit.Result{EventID: event.ID, Accepted: true}
}

// fakeAuditOutbox is an in-memory AuditOutboxStore.
type fakeAuditOutbox struct {
	rows      []*store.ApprovalOutboxEntry
	delivered []string
	listErr   error
	markErr   error
}

func (f *fakeAuditOutbox) ListUndelivered(_ context.Context, limit int) ([]*store.ApprovalOutboxEntry, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []*store.ApprovalOutboxEntry
	for _, row := range f.rows {
		if row.Delivered {
			continue
		}
		out = append(out, row)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeAuditOutbox) MarkDelivered(_ context.Context, id string, at time.Time) error {
	if f.markErr != nil {
		return f.markErr
	}
	for _, row := range f.rows {
		if row.ID == id {
			row.Delivered = true
			row.DeliveredAt = &at
		}
	}
	f.delivered = append(f.delivered, id)
	return nil
}

func auditWorkerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func approvalOutboxRow(t *testing.T, id, eventType string, payload map[string]any) *store.ApprovalOutboxEntry {
	t.Helper()
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	return &store.ApprovalOutboxEntry{
		ID: id, EventType: eventType, PayloadJSON: encoded,
		CreatedAt: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC),
	}
}

// TASK-231: an approval outbox row must reach the audit pipeline (the desensitization path) and
// only then be acknowledged.
func TestAuditOutboxWorkerDeliversAndAcknowledges(t *testing.T) {
	row := approvalOutboxRow(t, "outbox-1", "values_revision.approved", map[string]any{
		"event_id":              "event-1",
		"revision_id":           "vr-1",
		"release_definition_id": "def-1",
		"organization_id":       "org-1",
		"request_id":            "req-1",
		"state":                 "approved",
		"actor_user_id":         "user-1",
		"actor_role":            "release-admin",
		"occurred_at":           "2026-09-30T09:59:00Z",
	})
	outbox := &fakeAuditOutbox{rows: []*store.ApprovalOutboxEntry{row}}
	sink := &rejectingAuditSink{}
	worker := NewAuditOutboxWorker(outbox, sink, auditWorkerLogger(), AuditOutboxWorkerConfig{BatchSize: 10})

	delivered, err := worker.DrainOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, delivered)
	require.Len(t, sink.events, 1, "the row must reach the audit pipeline")

	event := sink.events[0]
	assert.Equal(t, "event-1", event.ID)
	assert.Equal(t, store.AuditActorUser, event.ActorKind)
	assert.Equal(t, "user-1", event.ActorID)
	assert.Equal(t, "org-1", event.OrganizationID)
	assert.Equal(t, "release-admin", event.Role)
	assert.Equal(t, "values_revision", event.ResourceType)
	assert.Equal(t, "vr-1", event.ResourceID)
	assert.Equal(t, "values_revision.approved", event.Action)
	assert.Equal(t, "approved", event.Status)
	assert.Equal(t, time.Date(2026, 9, 30, 9, 59, 0, 0, time.UTC), event.CreatedAt)
	assert.Equal(t, "def-1", event.Metadata["release_definition_id"])
	assert.Equal(t, "req-1", event.Metadata["request_id"])

	assert.Equal(t, []string{"outbox-1"}, outbox.delivered, "an accepted entry is acknowledged")
}

// A rejected entry stays queued: acknowledging it would delete the audit record, which is the
// failure this worker exists to fix.
func TestAuditOutboxWorkerKeepsRejectedEntriesQueued(t *testing.T) {
	row := approvalOutboxRow(t, "outbox-2", "values_revision.rejected", map[string]any{
		"event_id": "event-2", "revision_id": "vr-2",
	})
	outbox := &fakeAuditOutbox{rows: []*store.ApprovalOutboxEntry{row}}
	sink := &rejectingAuditSink{reject: map[string]bool{"event-2": true}}
	worker := NewAuditOutboxWorker(outbox, sink, auditWorkerLogger(), AuditOutboxWorkerConfig{})

	delivered, err := worker.DrainOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Empty(t, outbox.delivered, "a rejected entry must not be acknowledged")

	_, err = worker.DrainOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, sink.attempt, "the entry is retried, not dropped")
}

// An entry whose payload cannot be parsed still produces an audit record: an unrecognized shape
// must not vanish from the trail.
func TestAuditOutboxWorkerEmitsUnparsedPayloads(t *testing.T) {
	row := &store.ApprovalOutboxEntry{
		ID: "outbox-3", EventType: "release_bundle.created", PayloadJSON: []byte("not json"),
		CreatedAt: time.Now().UTC(),
	}
	outbox := &fakeAuditOutbox{rows: []*store.ApprovalOutboxEntry{row}}
	sink := &rejectingAuditSink{}
	worker := NewAuditOutboxWorker(outbox, sink, auditWorkerLogger(), AuditOutboxWorkerConfig{})

	delivered, err := worker.DrainOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, delivered)
	require.Len(t, sink.events, 1)
	assert.Equal(t, "release_bundle.created", sink.events[0].Action)
	assert.Equal(t, "true", sink.events[0].Metadata["payload_unparsed"])
	assert.Equal(t, []string{"outbox-3"}, outbox.delivered)
}

// Without a sink nothing can be delivered, and nothing may be acknowledged either.
func TestAuditOutboxWorkerFailsClosedWithoutSink(t *testing.T) {
	outbox := &fakeAuditOutbox{rows: []*store.ApprovalOutboxEntry{approvalOutboxRow(t, "outbox-4", "x.y", map[string]any{})}}
	worker := NewAuditOutboxWorker(outbox, nil, auditWorkerLogger(), AuditOutboxWorkerConfig{})
	_, err := worker.DrainOnce(context.Background())
	require.Error(t, err)
	assert.Empty(t, outbox.delivered)
}

// A read failure is reported and acknowledges nothing.
func TestAuditOutboxWorkerPropagatesListFailure(t *testing.T) {
	outbox := &fakeAuditOutbox{listErr: errors.New("boom")}
	worker := NewAuditOutboxWorker(outbox, &rejectingAuditSink{}, auditWorkerLogger(), AuditOutboxWorkerConfig{})
	_, err := worker.DrainOnce(context.Background())
	require.Error(t, err)
	assert.Empty(t, outbox.delivered)
}
