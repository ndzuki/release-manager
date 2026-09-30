//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TASK-231: the PostgreSQL half of the audit-outbox drain.
func TestAuditOutboxListAndAcknowledge(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	pg := st.GORM().Exec(`
		INSERT INTO audit_outbox (id, event_type, payload_json, created_at, delivered, delivered_at)
		VALUES (?, ?, ?::jsonb, ?, FALSE, NULL), (?, ?, ?::jsonb, ?, FALSE, NULL), (?, ?, ?::jsonb, ?, TRUE, ?)
	`,
		"audit-1", "values_revision.approved", `{"event_id":"e1"}`, now,
		"audit-2", "values_revision.rejected", `{"event_id":"e2"}`, now.Add(time.Second),
		"audit-done", "values_revision.submitted", `{"event_id":"e3"}`, now, now)
	require.NoError(t, pg.Error)

	undelivered, err := st.AuditOutbox().ListUndelivered(ctx, 10)
	require.NoError(t, err)
	require.Len(t, undelivered, 2, "only undelivered rows are queued")
	assert.Equal(t, "audit-1", undelivered[0].ID, "oldest first")
	assert.Equal(t, "audit-2", undelivered[1].ID)
	assert.JSONEq(t, `{"event_id":"e1"}`, string(undelivered[0].PayloadJSON))

	limited, err := st.AuditOutbox().ListUndelivered(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, limited, 1)

	require.NoError(t, st.AuditOutbox().MarkDelivered(ctx, "audit-1", now))
	after, err := st.AuditOutbox().ListUndelivered(ctx, 10)
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, "audit-2", after[0].ID)

	require.NoError(t, st.AuditOutbox().MarkDelivered(ctx, "audit-1", now), "acknowledging twice is not an error")
	require.Error(t, st.AuditOutbox().MarkDelivered(ctx, "audit-missing", now))
}
