package audit

import (
	"context"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// AC-029-05: an undecodable cursor must stay distinguishable from an internal
// failure, so a client can tell "your page token is stale" from "the server
// broke". The store already returns ErrInvalidCursor for it; the handler used to
// collapse every query error into CodeInternal.
func TestQueryAuditEventsInvalidCursorIsClientError(t *testing.T) {
	st, err := sqlitestore.Open(t.TempDir() + "/audit-cursor.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	sink := sinkFunc(func(string) Result { return Result{Accepted: true} })
	handler := NewAuditServiceHandler(st, sink, slog.New(slog.DiscardHandler), allowAll())

	ctx := context.WithValue(context.Background(), principalContextKey{}, Principal{
		UserID: "user-1", OrgID: "org-1", Roles: []string{string(store.RoleViewer)}, Authorization: "Bearer user-token",
	})
	_, err = handler.QueryAuditEvents(ctx, connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Pagination: &commonv1.Pagination{PageToken: "not-a-cursor"},
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "invalid_cursor", connectErr.Meta().Get("X-Reason-Code"),
		"clients branch on the reason code, not on the message text")
}
