package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TASK-231: the audit outbox needs a consumer. Nothing read this table before, so an approval
// audit stopped at the outbox and never reached the audit query surface.
func TestAuditOutboxListAndAcknowledge(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	// The rows are written by the transition's own transaction; this seeds them the same way.
	_, err := st.DB().ExecContext(ctx, `
		INSERT INTO audit_outbox (id, event_type, payload_json, created_at, delivered, delivered_at)
		VALUES (?, ?, ?, ?, 0, NULL), (?, ?, ?, ?, 0, NULL), (?, ?, ?, ?, 1, ?)
	`,
		"audit-1", "values_revision.approved", `{"event_id":"e1"}`, now.Format(time.RFC3339Nano),
		"audit-2", "values_revision.rejected", `{"event_id":"e2"}`, now.Add(time.Second).Format(time.RFC3339Nano),
		"audit-done", "values_revision.submitted", `{"event_id":"e3"}`, now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano))
	require.NoError(t, err)

	undelivered, err := st.AuditOutbox().ListUndelivered(ctx, 10)
	require.NoError(t, err)
	require.Len(t, undelivered, 2, "only undelivered rows are queued")
	assert.Equal(t, "audit-1", undelivered[0].ID, "oldest first")
	assert.Equal(t, "audit-2", undelivered[1].ID)
	assert.Equal(t, "values_revision.approved", undelivered[0].EventType)
	assert.JSONEq(t, `{"event_id":"e1"}`, string(undelivered[0].PayloadJSON))

	// The batch limit bounds one drain pass.
	limited, err := st.AuditOutbox().ListUndelivered(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, limited, 1)

	require.NoError(t, st.AuditOutbox().MarkDelivered(ctx, "audit-1", now))
	after, err := st.AuditOutbox().ListUndelivered(ctx, 10)
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, "audit-2", after[0].ID)

	// Acknowledging twice is not an error: at-least-once delivery repeats acknowledgements.
	require.NoError(t, st.AuditOutbox().MarkDelivered(ctx, "audit-1", now))
	// An unknown id is not silently accepted.
	require.Error(t, st.AuditOutbox().MarkDelivered(ctx, "audit-missing", now))
}
