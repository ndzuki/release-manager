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
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// countingStore proves a denied request never reaches persistence. It embeds the
// real interfaces so it satisfies store.Store / store.AuditEventStore while only
// the methods the handler could touch are implemented.
type countingStore struct {
	store.Store
	queryCalls int
	countCalls int
	lastFilter store.AuditEventFilter
}

func (s *countingStore) AuditEvents() store.AuditEventStore {
	return &countingAuditEvents{parent: s}
}

type countingAuditEvents struct {
	store.AuditEventStore
	parent *countingStore
}

func (c *countingAuditEvents) Query(_ context.Context, filter store.AuditEventFilter, _ string, _ int) (*store.AuditEventPage, error) {
	c.parent.queryCalls++
	c.parent.lastFilter = filter
	return &store.AuditEventPage{}, nil
}

func (c *countingAuditEvents) Count(_ context.Context, _ store.AuditEventFilter) (int64, error) {
	c.parent.countCalls++
	return 0, nil
}

func auditTestPrincipalCtx() context.Context {
	return context.WithValue(context.Background(), principalContextKey{}, Principal{
		UserID: "user-1", OrgID: "org-1", Roles: []string{string(store.RoleViewer)}, Authorization: "Bearer user-token",
	})
}

func newAllOrgsHandler(st store.Store, client DecisionClient) auditv1connect.AuditServiceHandler {
	sink := sinkFunc(func(string) Result { return Result{Accepted: true} })
	return NewAuditServiceHandler(st, sink, slog.New(slog.DiscardHandler), client)
}

// TestQueryAllOrganizationsForPlatformAdmin is AC-339-01/03: an authorized
// platform_admin request widens the read to every organization, including the
// platform-level "system" scope, and the filter handed to the store carries no
// organization predicate even when the request still named one.
func TestQueryAllOrganizationsForPlatformAdmin(t *testing.T) {
	st, err := sqlitestore.Open(t.TempDir() + "/audit-all-orgs.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	now := time.Now().UTC()
	for _, event := range []*store.AuditEvent{
		{ID: "event-org-1", ActorKind: store.AuditActorUser, ActorID: "user-1", OrganizationID: "org-1",
			ResourceType: "release", ResourceID: "rel-1", Action: "create", Status: "success", CreatedAt: now.Add(-2 * time.Second)},
		{ID: "event-org-2", ActorKind: store.AuditActorUser, ActorID: "user-2", OrganizationID: "org-2",
			ResourceType: "release", ResourceID: "rel-2", Action: "create", Status: "success", CreatedAt: now.Add(-time.Second)},
		{ID: "event-system", ActorKind: store.AuditActorSystem, ActorID: "cleanup", OrganizationID: store.SystemOrganizationID,
			Role: "system", ResourceType: "gc_cycle", ResourceID: "cycle-1", Action: "cleanup.gc", Status: "succeeded", CreatedAt: now},
	} {
		require.NoError(t, st.AuditEvents().CreateBatch(t.Context(), []*store.AuditEvent{event}))
	}

	handler := newAllOrgsHandler(st, &stubDecisionClient{decision: AccessDecision{
		Allowed: true, Reason: "ok", OrganizationID: "org-1",
		AllowCrossOrganization: true, MaxWindowDays: 366, PolicyVersion: 9,
	}})
	response, err := handler.QueryAuditEvents(auditTestPrincipalCtx(), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{OrganizationId: "org-1", AllOrganizations: true},
	}))
	require.NoError(t, err)

	ids := make([]string, 0, len(response.Msg.GetEvents()))
	for _, event := range response.Msg.GetEvents() {
		ids = append(ids, event.GetId())
	}
	assert.ElementsMatch(t, []string{"event-org-1", "event-org-2", "event-system"}, ids,
		"all_organizations must include events outside the requested and the session organization")
	assert.Equal(t, int32(3), response.Msg.GetPagination().GetTotalSize(),
		"the count must use the same all-organizations scope as the page")
}

// TestQueryAllOrganizationsDeniedForNonPlatformAdmin is the AC-339-02 security
// gate: the widening is refused with the stable reason code, and the store is
// never queried (not "queried then filtered").
func TestQueryAllOrganizationsDeniedForNonPlatformAdmin(t *testing.T) {
	st := &countingStore{}
	handler := newAllOrgsHandler(st, &stubDecisionClient{decision: AccessDecision{
		Allowed: true, Reason: "ok", OrganizationID: "org-1",
		AllowCrossOrganization: false, MaxWindowDays: 31, PolicyVersion: 3,
	}})

	_, err := handler.QueryAuditEvents(auditTestPrincipalCtx(), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{AllOrganizations: true},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "cross_organization_denied", connectErr.Meta().Get("X-Reason-Code"))
	assert.Zero(t, st.queryCalls, "a denied all-organizations query must not reach the store")
	assert.Zero(t, st.countCalls, "a denied all-organizations query must not reach the store count")
}

// TestQueryForeignOrganizationDenied covers the sibling boundary: naming another
// organization without a cross-organization decision is denied before any store
// access.
func TestQueryForeignOrganizationDenied(t *testing.T) {
	st := &countingStore{}
	handler := newAllOrgsHandler(st, &stubDecisionClient{decision: AccessDecision{
		Allowed: false, Reason: "cross_organization_denied", OrganizationID: "org-2", MaxWindowDays: 31,
	}})

	_, err := handler.QueryAuditEvents(auditTestPrincipalCtx(), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{OrganizationId: "org-2"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "cross_organization_denied", connectErr.Meta().Get("X-Reason-Code"))
	assert.Zero(t, st.queryCalls, "a denied foreign-organization query must not reach the store")
}

// TestQueryEmptyDecisionOrganizationFailsClosed pins the invariant that "no
// effective organization" is an authorization failure, never "all tenants".
func TestQueryEmptyDecisionOrganizationFailsClosed(t *testing.T) {
	for _, allOrganizations := range []bool{false, true} {
		t.Run(map[bool]string{false: "own organization", true: "all organizations"}[allOrganizations], func(t *testing.T) {
			st := &countingStore{}
			handler := newAllOrgsHandler(st, &stubDecisionClient{decision: AccessDecision{
				Allowed: true, Reason: "ok", OrganizationID: "",
				AllowCrossOrganization: true, MaxWindowDays: 366,
			}})

			_, err := handler.QueryAuditEvents(auditTestPrincipalCtx(), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
				Filter: &auditv1.AuditQueryFilter{AllOrganizations: allOrganizations},
			}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
			assert.Zero(t, st.queryCalls, "an empty decision scope must fail before the store")
		})
	}
}

// TestExportRejectsAllOrganizations makes the union scope explicit on the export
// path: an export row holds one organization, so all_organizations must be
// rejected rather than silently ignored.
func TestExportRejectsAllOrganizations(t *testing.T) {
	st := &countingStore{}
	handler := newAllOrgsHandler(st, &stubDecisionClient{decision: AccessDecision{
		Allowed: true, Reason: "ok", OrganizationID: "org-1",
		AllowCrossOrganization: true, MaxWindowDays: 366, PolicyVersion: 9,
	}})

	_, err := handler.ExportAuditEvents(auditTestPrincipalCtx(), connect.NewRequest(&auditv1.ExportAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{AllOrganizations: true},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "all_organizations_not_supported", connectErr.Meta().Get("X-Reason-Code"))
}

// TestQueryEmptyFilterKeepsOwnOrganizationSemantics pins AC-339-03's non-change:
// the new field does not alter what an empty filter means.
func TestQueryEmptyFilterKeepsOwnOrganizationSemantics(t *testing.T) {
	st := &countingStore{}
	handler := newAllOrgsHandler(st, &stubDecisionClient{decision: AccessDecision{
		Allowed: true, Reason: "ok", OrganizationID: "org-1", MaxWindowDays: 31,
	}})

	_, err := handler.QueryAuditEvents(auditTestPrincipalCtx(), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{},
	}))
	require.NoError(t, err)
	assert.Equal(t, 1, st.queryCalls)
	assert.Equal(t, "org-1", st.lastFilter.OrganizationID,
		"an empty filter must still resolve to the decision's own organization")
}

// TestQueryAllOrganizationsClearsStoreFilter proves the widening reuses the
// store's existing "no organization predicate" path instead of adding a second
// query shape: the filter reaching the store has an empty organization even when
// the request named one.
func TestQueryAllOrganizationsClearsStoreFilter(t *testing.T) {
	st := &countingStore{}
	handler := newAllOrgsHandler(st, &stubDecisionClient{decision: AccessDecision{
		Allowed: true, Reason: "ok", OrganizationID: "org-2",
		AllowCrossOrganization: true, MaxWindowDays: 366,
	}})

	_, err := handler.QueryAuditEvents(auditTestPrincipalCtx(), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{OrganizationId: "org-2", AllOrganizations: true},
	}))
	require.NoError(t, err)
	assert.Equal(t, 1, st.queryCalls)
	assert.Empty(t, st.lastFilter.OrganizationID,
		"all_organizations must be expressed as an empty organization predicate")
}
