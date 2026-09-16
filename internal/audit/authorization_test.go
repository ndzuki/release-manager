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
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// TestAuditServiceHandlerScopesToPrincipalOrganization is the TASK-095 AC-4
// gate for the release-api audit surface: the JWT principal's organization (not
// the request) decides what can be read, exported, or emitted.
func TestAuditServiceHandlerScopesToPrincipalOrganization(t *testing.T) {
	st, err := sqlitestore.Open(t.TempDir() + "/audit-authz.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	now := time.Now().UTC()
	for _, event := range []*store.AuditEvent{
		{ID: "event-org-1", ActorKind: store.AuditActorUser, ActorID: "user-1", OrganizationID: "org-1",
			ResourceType: "release", ResourceID: "rel-1", Action: "create", Status: "success", CreatedAt: now},
		{ID: "event-org-2", ActorKind: store.AuditActorUser, ActorID: "user-2", OrganizationID: "org-2",
			ResourceType: "release", ResourceID: "rel-2", Action: "create", Status: "success", CreatedAt: now},
	} {
		require.NoError(t, st.AuditEvents().Create(t.Context(), event))
	}

	emitted := 0
	sink := sinkFunc(func(string) Result { emitted++; return Result{Accepted: true} })
	handler := NewAuditServiceHandler(st, sink, slog.New(slog.DiscardHandler))
	principalCtx := func(orgID string) context.Context {
		return context.WithValue(context.Background(), principalContextKey{}, Principal{
			UserID: "user-1", OrgID: orgID, Roles: []string{string(store.RoleReleaseAdmin)},
		})
	}

	t.Run("query defaults to the principal organization", func(t *testing.T) {
		response, err := handler.QueryAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.QueryAuditEventsRequest{}))
		require.NoError(t, err)
		require.Len(t, response.Msg.GetEvents(), 1)
		assert.Equal(t, "event-org-1", response.Msg.GetEvents()[0].GetId())
	})

	t.Run("query of another organization is denied", func(t *testing.T) {
		_, err := handler.QueryAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
			Filter: &auditv1.AuditQueryFilter{OrganizationId: "org-2"},
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})

	t.Run("emit into another organization is denied", func(t *testing.T) {
		_, err := handler.Emit(principalCtx("org-1"), connect.NewRequest(&auditv1.EmitAuditRequest{
			Events: []*auditv1.AuditEvent{{
				Id: "forged", Actor: &auditv1.AuditActor{Kind: auditv1.ActorKind_ACTOR_KIND_USER, Id: "user-1", OrganizationId: "org-2"},
				ResourceType: "release", Action: "create", Status: "success",
			}},
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		assert.Zero(t, emitted, "a denied event must not reach the emitter")
	})

	t.Run("emit into the principal organization is accepted", func(t *testing.T) {
		response, err := handler.Emit(principalCtx("org-1"), connect.NewRequest(&auditv1.EmitAuditRequest{
			Events: []*auditv1.AuditEvent{{
				Id: "legitimate", Actor: &auditv1.AuditActor{Kind: auditv1.ActorKind_ACTOR_KIND_USER, Id: "user-1", OrganizationId: "org-1"},
				ResourceType: "release", Action: "create", Status: "success",
			}},
		}))
		require.NoError(t, err)
		assert.Equal(t, int32(1), response.Msg.GetAccepted())
	})

	t.Run("export of another organization is denied", func(t *testing.T) {
		_, err := handler.ExportAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.ExportAuditEventsRequest{
			Filter: &auditv1.AuditQueryFilter{OrganizationId: "org-2"},
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})

	t.Run("export is recorded against the principal organization", func(t *testing.T) {
		response, err := handler.ExportAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.ExportAuditEventsRequest{}))
		require.NoError(t, err)
		assert.NotEmpty(t, response.Msg.GetExportId())

		page, err := st.AuditEvents().Query(t.Context(), store.AuditEventFilter{
			OrganizationID: "org-1", Action: "export.created",
		}, "", 10)
		require.NoError(t, err)
		require.Len(t, page.Events, 1)
		assert.Equal(t, "org-1", page.Events[0].OrganizationID)
		assert.Equal(t, store.AuditActorSystem, page.Events[0].ActorKind)

		other, err := st.AuditEvents().Query(t.Context(), store.AuditEventFilter{
			OrganizationID: "org-2", Action: "export.created",
		}, "", 10)
		require.NoError(t, err)
		assert.Empty(t, other.Events)
	})

	t.Run("requests without a principal are unauthenticated", func(t *testing.T) {
		_, err := handler.QueryAuditEvents(context.Background(), connect.NewRequest(&auditv1.QueryAuditEventsRequest{}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

		_, err = handler.Emit(context.Background(), connect.NewRequest(&auditv1.EmitAuditRequest{
			Events: []*auditv1.AuditEvent{{Id: "no-principal", ResourceType: "release", Action: "create"}},
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("principal without an organization is unauthenticated", func(t *testing.T) {
		_, err := handler.QueryAuditEvents(principalCtx(""), connect.NewRequest(&auditv1.QueryAuditEventsRequest{}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})
}
