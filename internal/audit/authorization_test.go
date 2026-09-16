package audit

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// stubDecisionClient records what the handler asked release-auth for and answers
// with a canned decision (ADR-021).
type stubDecisionClient struct {
	decision AccessDecision
	err      error

	calls      int
	lastAuth   string
	lastOrg    string
	lastObject string
	lastAction string
}

func (s *stubDecisionClient) Authorize(
	_ context.Context, authorization, organizationID, object, action string,
) (AccessDecision, error) {
	s.calls++
	s.lastAuth, s.lastOrg, s.lastObject, s.lastAction = authorization, organizationID, object, action
	if s.err != nil {
		return AccessDecision{}, s.err
	}
	decision := s.decision
	if decision.OrganizationID == "" {
		decision.OrganizationID = organizationID
	}
	return decision, nil
}

func allowAll() *stubDecisionClient {
	return &stubDecisionClient{decision: AccessDecision{
		Allowed: true, Reason: "ok", OrganizationID: "org-1", MaxWindowDays: 31, PolicyVersion: 7,
	}}
}

// TestAuditServiceHandlerUsesReleaseAuthDecision is the TASK-103 gate: the audit
// surface no longer decides roles locally, it applies what release-auth returns,
// and it fails closed when the decision cannot be obtained (ADR-021).
func TestAuditServiceHandlerUsesReleaseAuthDecision(t *testing.T) {
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
	handlerFor := func(client DecisionClient) auditv1connect.AuditServiceHandler {
		return NewAuditServiceHandler(st, sink, slog.New(slog.DiscardHandler), client)
	}
	principalCtx := func(orgID string) context.Context {
		return context.WithValue(context.Background(), principalContextKey{}, Principal{
			UserID: "user-1", OrgID: orgID, Roles: []string{string(store.RoleViewer)}, Authorization: "Bearer user-token",
		})
	}

	t.Run("query applies the decision scope and forwards the caller token", func(t *testing.T) {
		client := allowAll()
		handler := handlerFor(client)
		response, err := handler.QueryAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.QueryAuditEventsRequest{}))
		require.NoError(t, err)
		require.Len(t, response.Msg.GetEvents(), 1)
		assert.Equal(t, "event-org-1", response.Msg.GetEvents()[0].GetId())
		assert.Equal(t, "Bearer user-token", client.lastAuth, "the caller's own token must be forwarded")
		assert.Equal(t, auditObject, client.lastObject)
		assert.Equal(t, auditRead, client.lastAction)
	})

	t.Run("platform_admin cross-organization scope comes from the decision", func(t *testing.T) {
		handler := handlerFor(&stubDecisionClient{decision: AccessDecision{
			Allowed: true, Reason: "ok", OrganizationID: "org-2",
			AllowCrossOrganization: true, MaxWindowDays: 366, PolicyVersion: 9,
		}})
		response, err := handler.QueryAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
			Filter: &auditv1.AuditQueryFilter{OrganizationId: "org-2"},
		}))
		require.NoError(t, err)
		require.Len(t, response.Msg.GetEvents(), 1)
		assert.Equal(t, "event-org-2", response.Msg.GetEvents()[0].GetId())
	})

	t.Run("denied decision maps to permission_denied with the reason code", func(t *testing.T) {
		handler := handlerFor(&stubDecisionClient{decision: AccessDecision{
			Allowed: false, Reason: "cross_organization_denied", OrganizationID: "org-2", MaxWindowDays: 31,
		}})
		_, err := handler.QueryAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
			Filter: &auditv1.AuditQueryFilter{OrganizationId: "org-2"},
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		var connectErr *connect.Error
		require.ErrorAs(t, err, &connectErr)
		assert.Equal(t, "cross_organization_denied", connectErr.Meta().Get("X-Reason-Code"))
	})

	t.Run("range beyond the policy ceiling is range_too_large", func(t *testing.T) {
		handler := handlerFor(allowAll())
		_, err := handler.QueryAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
			Filter: &auditv1.AuditQueryFilter{TimeRange: &commonv1.TimestampRange{
				Start: timestamppb.New(now.Add(-40 * 24 * time.Hour)),
				End:   timestamppb.New(now),
			}},
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		var connectErr *connect.Error
		require.ErrorAs(t, err, &connectErr)
		assert.Equal(t, "range_too_large", connectErr.Meta().Get("X-Reason-Code"))
	})

	t.Run("platform_admin window ceiling allows the wider range", func(t *testing.T) {
		handler := handlerFor(&stubDecisionClient{decision: AccessDecision{
			Allowed: true, Reason: "ok", OrganizationID: "org-1", AllowCrossOrganization: true, MaxWindowDays: 366,
		}})
		_, err := handler.QueryAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
			Filter: &auditv1.AuditQueryFilter{TimeRange: &commonv1.TimestampRange{
				Start: timestamppb.New(now.Add(-40 * 24 * time.Hour)),
				End:   timestamppb.New(now),
			}},
		}))
		require.NoError(t, err)
	})

	t.Run("emit requires a write decision", func(t *testing.T) {
		client := allowAll()
		handler := handlerFor(client)
		response, err := handler.Emit(principalCtx("org-1"), connect.NewRequest(&auditv1.EmitAuditRequest{
			Events: []*auditv1.AuditEvent{{
				Id: "legitimate", Actor: &auditv1.AuditActor{Kind: auditv1.ActorKind_ACTOR_KIND_USER, Id: "user-1", OrganizationId: "org-1"},
				ResourceType: "release", Action: "create", Status: "success",
			}},
		}))
		require.NoError(t, err)
		assert.Equal(t, int32(1), response.Msg.GetAccepted())
		assert.Equal(t, auditWrite, client.lastAction)
	})

	t.Run("emit into another organization is denied", func(t *testing.T) {
		before := emitted
		handler := handlerFor(allowAll())
		_, err := handler.Emit(principalCtx("org-1"), connect.NewRequest(&auditv1.EmitAuditRequest{
			Events: []*auditv1.AuditEvent{{
				Id: "forged", Actor: &auditv1.AuditActor{Kind: auditv1.ActorKind_ACTOR_KIND_USER, Id: "user-1", OrganizationId: "org-2"},
				ResourceType: "release", Action: "create", Status: "success",
			}},
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		assert.Equal(t, before, emitted, "a denied event must not reach the emitter")
	})

	t.Run("export is recorded against the decision organization", func(t *testing.T) {
		handler := handlerFor(allowAll())
		response, err := handler.ExportAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.ExportAuditEventsRequest{}))
		require.NoError(t, err)
		assert.NotEmpty(t, response.Msg.GetExportId())

		page, err := st.AuditEvents().Query(t.Context(), store.AuditEventFilter{
			OrganizationID: "org-1", Action: "export.created",
		}, "", 10)
		require.NoError(t, err)
		require.Len(t, page.Events, 1)
		assert.Equal(t, "org-1", page.Events[0].OrganizationID)
	})

	t.Run("unavailable decision authority fails closed", func(t *testing.T) {
		handler := handlerFor(&stubDecisionClient{err: errors.New("auth unreachable")})
		_, err := handler.QueryAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.QueryAuditEventsRequest{}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

		_, err = handler.Emit(principalCtx("org-1"), connect.NewRequest(&auditv1.EmitAuditRequest{
			Events: []*auditv1.AuditEvent{{Id: "no-authz", ResourceType: "release", Action: "create"}},
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	})

	t.Run("a nil decision client fails closed", func(t *testing.T) {
		handler := handlerFor(nil)
		_, err := handler.QueryAuditEvents(principalCtx("org-1"), connect.NewRequest(&auditv1.QueryAuditEventsRequest{}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	})

	t.Run("requests without a principal are unauthenticated", func(t *testing.T) {
		handler := handlerFor(allowAll())
		_, err := handler.QueryAuditEvents(context.Background(), connect.NewRequest(&auditv1.QueryAuditEventsRequest{}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("principal without an organization is unauthenticated", func(t *testing.T) {
		handler := handlerFor(allowAll())
		_, err := handler.QueryAuditEvents(principalCtx(""), connect.NewRequest(&auditv1.QueryAuditEventsRequest{}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})
}
