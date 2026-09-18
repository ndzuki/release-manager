package audit

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// auditTestHandler builds a handler over a throwaway store with an allowing
// release-auth stub.
func auditTestHandler(t *testing.T) (auditv1connect.AuditServiceHandler, *sqlitestore.Store, context.Context) {
	t.Helper()
	st, err := sqlitestore.Open(t.TempDir() + "/audit-evidence.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	sink := sinkFunc(func(string) Result { return Result{Accepted: true} })
	handler := NewAuditServiceHandler(st, sink, slog.New(slog.DiscardHandler), allowAll())
	ctx := context.WithValue(context.Background(), principalContextKey{}, Principal{
		UserID: "user-1", OrgID: "org-1", Roles: []string{string(store.RolePlatformAdmin)}, Authorization: "Bearer user-token",
	})
	return handler, st, ctx
}

// seedAuditEvent writes one event, spaced a second apart so the created_at/id
// keyset has a deterministic order.
func seedAuditEvent(ctx context.Context, t *testing.T, st *sqlitestore.Store, id string, at time.Time) {
	t.Helper()
	require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
		ID: id, ActorKind: store.AuditActorSystem, ActorID: "system", OrganizationID: "org-1",
		Action: "create", Status: "success", CreatedAt: at,
	}))
}

// AC-029-03: an event written between two pages must not shift the second page,
// so no event is returned twice.
func TestQueryAuditEventsCursorIsStableAcrossInserts(t *testing.T) {
	handler, st, ctx := auditTestHandler(t)
	base := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	seedAuditEvent(ctx, t, st, "ev-1", base)
	seedAuditEvent(ctx, t, st, "ev-2", base.Add(time.Second))
	seedAuditEvent(ctx, t, st, "ev-3", base.Add(2*time.Second))

	first, err := handler.QueryAuditEvents(ctx, connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Pagination: &commonv1.Pagination{PageSize: 2},
	}))
	require.NoError(t, err)
	require.Len(t, first.Msg.GetEvents(), 2)
	cursor := first.Msg.GetPagination().GetNextPageToken()
	require.NotEmpty(t, cursor)

	// A new event lands between the two page reads.
	seedAuditEvent(ctx, t, st, "ev-inserted", base.Add(3*time.Second))

	second, err := handler.QueryAuditEvents(ctx, connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Pagination: &commonv1.Pagination{PageSize: 2, PageToken: cursor},
	}))
	require.NoError(t, err)

	seen := map[string]bool{}
	for _, event := range first.Msg.GetEvents() {
		seen[event.GetId()] = true
	}
	for _, event := range second.Msg.GetEvents() {
		assert.False(t, seen[event.GetId()], "AC-029-03: page 2 must not repeat %s", event.GetId())
		seen[event.GetId()] = true
	}
	assert.True(t, seen["ev-3"], "the event that existed at page 1 must still be reachable")
}

// AC-029-04: creating an export writes its own audit event, so the export itself
// is on the trail.
func TestExportAuditEventsWritesAnAuditEvent(t *testing.T) {
	handler, st, ctx := auditTestHandler(t)

	resp, err := handler.ExportAuditEvents(ctx, connect.NewRequest(&auditv1.ExportAuditEventsRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, resp.Msg.GetExportId(), "the receipt carries the export id")

	events, err := st.AuditEvents().ListByResource(ctx, "audit_export", resp.Msg.GetExportId())
	require.NoError(t, err)
	require.Len(t, events, 1, "AC-029-04: the export must appear on the audit trail")
	assert.Equal(t, "export.created", events[0].Action)
}
