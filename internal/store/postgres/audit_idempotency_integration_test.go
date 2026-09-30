//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TestAuditEventInsertIsIdempotentByID_Postgres is the PostgreSQL half of the
// REQ-050/TASK-097 AC4 evidence: the event id is the deduplication key, so a
// replay must neither fail the statement nor the batch nor store a second row.
// The SQLite adapter has the same case in internal/store/sqlite/audit_test.go;
// TASK-097 recorded "behaviour evidence on SQLite only" as a known limit, and
// this test closes it. It runs only with POSTGRES_TEST_DSN set
// (setupStore creates a per-test schema and runs migrations).
func TestAuditEventInsertIsIdempotentByID_Postgres(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	event := &store.AuditEvent{
		ID: "event-idempotent-pg", ActorKind: store.AuditActorSystem, ActorID: "system",
		OrganizationID: "org-1", ResourceType: "operator", ResourceID: "op-1",
		Action: "operator.revoked", Status: "succeeded", CreatedAt: time.Now().UTC(),
	}

	require.NoError(t, st.AuditEvents().CreateBatch(ctx, []*store.AuditEvent{event}))

	// A replay with different content must be a no-op, not an error and not an
	// overwrite (ON CONFLICT (id) DO NOTHING).
	replay := *event
	replay.Action = "operator.revoked.again"
	require.NoError(t, st.AuditEvents().CreateBatch(ctx, []*store.AuditEvent{&replay}), "a duplicate id must not fail the insert")

	// Read back HERE, before the batch below re-inserts the original value: a
	// last-write-wins implementation would otherwise be masked by that batch restoring
	// this field, which is how this assertion used to pass without discriminating.
	afterReplay, err := st.AuditEvents().GetByID(ctx, event.ID)
	require.NoError(t, err)
	assert.Equal(t, "operator.revoked", afterReplay.Action, "a replay must not overwrite the first write")

	require.NoError(t, st.AuditEvents().CreateBatch(ctx, []*store.AuditEvent{event, event}),
		"a batch containing duplicates must not fail")

	stored, err := st.AuditEvents().GetByID(ctx, event.ID)
	require.NoError(t, err)
	assert.Equal(t, "operator.revoked", stored.Action, "the first write wins; a replay does not overwrite")

	page, err := st.AuditEvents().Query(ctx, store.AuditEventFilter{ResourceType: "operator", ResourceID: "op-1"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, page.Events, 1, "the deduplication key is the event id")
}
