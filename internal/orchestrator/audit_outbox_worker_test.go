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

// The outbox is written by five different transitions and they name the actor and the timestamp
// differently. Mapping only the approval shape dropped the actor (and any tenant filter) for the
// rest, which review of TASK-231 measured end to end.
func TestAuditEventFromOutboxMapsEveryWriterShape(t *testing.T) {
	tests := []struct {
		name       string
		eventType  string
		payload    map[string]any
		wantActor  string
		wantOrg    string
		wantRole   string
		wantRes    string
		wantResID  string
		wantStatus string
		wantTime   time.Time
	}{
		{
			name: "values approval approved", eventType: "values_revision.approved",
			payload: map[string]any{
				"event_id": "e1", "revision_id": "vr-1", "organization_id": "org-1", "state": "approved",
				"actor_user_id": "user-1", "actor_role": "release-admin", "occurred_at": "2026-09-30T09:00:00Z",
			},
			wantActor: "user-1", wantOrg: "org-1", wantRole: "release-admin",
			wantRes: "values_revision", wantResID: "vr-1", wantStatus: "approved",
			wantTime: time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "values lifecycle created", eventType: "ValuesRevisionCreated",
			payload: map[string]any{
				"event_id": "e2", "revision_id": "vr-2", "organization_id": "org-2",
				"created_by_user_id": "user-2", "created_at": "2026-09-30T08:30:00Z",
			},
			wantActor: "user-2", wantOrg: "org-2", wantRes: "values_revision", wantResID: "vr-2",
			wantStatus: "succeeded", wantTime: time.Date(2026, 9, 30, 8, 30, 0, 0, time.UTC),
		},
		{
			name: "values lifecycle discarded", eventType: "ValuesRevisionDiscarded",
			payload: map[string]any{
				"event_id": "e3", "revision_id": "vr-3", "organization_id": "org-3",
				"decided_by_user_id": "user-3", "decided_at": "2026-09-30T08:45:00Z",
			},
			wantActor: "user-3", wantOrg: "org-3", wantRes: "values_revision", wantResID: "vr-3",
			wantStatus: "succeeded", wantTime: time.Date(2026, 9, 30, 8, 45, 0, 0, time.UTC),
		},
		{
			name: "bundle created", eventType: "release_bundle.created",
			payload: map[string]any{
				"bundle_id": "bundle-1", "digest": "sha256:abc",
				"organization_id": "org-4", "actor_user_id": "user-4",
			},
			wantActor: "user-4", wantOrg: "org-4", wantRes: "release_bundle", wantResID: "bundle-1",
			wantStatus: "succeeded",
		},
		{
			name: "artifact event recorded", eventType: "artifact_event.recorded",
			payload: map[string]any{"event_id": "e5", "source_id": "source-5"},
			// No revision or bundle: the event keeps the outbox row id as its resource, so the
			// record still has a stable identity.
			wantRes: "audit_outbox", wantResID: "outbox-artifact event recorded", wantStatus: "succeeded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := approvalOutboxRow(t, "outbox-"+tt.name, tt.eventType, tt.payload)
			event := auditEventFromOutbox(row)
			assert.Equal(t, tt.eventType, event.Action)
			assert.Equal(t, tt.wantActor, event.ActorID, "actor")
			assert.Equal(t, tt.wantOrg, event.OrganizationID, "organization (tenant scope)")
			assert.Equal(t, tt.wantRole, event.Role)
			assert.Equal(t, tt.wantRes, event.ResourceType)
			assert.Equal(t, tt.wantResID, event.ResourceID)
			assert.Equal(t, tt.wantStatus, event.Status)
			if !tt.wantTime.IsZero() {
				assert.Equal(t, tt.wantTime, event.CreatedAt)
			}
			if tt.wantActor == "" {
				assert.Equal(t, store.AuditActorSystem, event.ActorKind, "an actorless event stays a system event")
			} else {
				assert.Equal(t, store.AuditActorUser, event.ActorKind)
			}
		})
	}
}

// Acknowledging can fail after the event was emitted; the worker must report the partial count
// and the error rather than pretend the batch was delivered.
func TestAuditOutboxWorkerReportsAcknowledgementFailure(t *testing.T) {
	rows := []*store.ApprovalOutboxEntry{
		approvalOutboxRow(t, "outbox-5", "values_revision.approved", map[string]any{"event_id": "event-5"}),
		approvalOutboxRow(t, "outbox-6", "values_revision.approved", map[string]any{"event_id": "event-6"}),
	}
	outbox := &fakeAuditOutbox{rows: rows, markErr: errors.New("db down")}
	sink := &rejectingAuditSink{}
	worker := NewAuditOutboxWorker(outbox, sink, auditWorkerLogger(), AuditOutboxWorkerConfig{})

	delivered, err := worker.DrainOnce(context.Background())
	require.Error(t, err)
	assert.Zero(t, delivered)
	assert.Len(t, sink.events, 1, "the first event was emitted before the acknowledgement failed")
}
