package audit

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type recordingEmitter struct{}

func (*recordingEmitter) Emit(*store.AuditEvent) bool { return true }

func setupAuditService(t *testing.T) (*ServiceHandler, *sqlitestore.Store) {
	t.Helper()
	st, err := sqlitestore.Open(t.TempDir() + "/audit.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return NewAuditServiceHandler(st, &recordingEmitter{}, slog.Default()), st
}

func auditContext(principal Principal) context.Context {
	return context.WithValue(context.Background(), principalContextKey{}, principal)
}

func auditTimeRange(start, end time.Time) *commonv1.TimestampRange {
	return &commonv1.TimestampRange{
		Start: timestamppb.New(start),
		End:   timestamppb.New(end),
	}
}

func TestQueryAuditEventsReleaseAdminCannotQueryOtherOrganization(t *testing.T) {
	svc, _ := setupAuditService(t)
	now := time.Now().UTC()
	_, err := svc.QueryAuditEvents(auditContext(Principal{
		UserID: "user-001",
		Roles:  []string{string(store.RoleReleaseAdmin)},
		OrgID:  "org-001",
	}), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{
			OrganizationId: "org-002",
			TimeRange:      auditTimeRange(now.Add(-time.Hour), now),
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, "permission_denied", errorReason(t, err))
}

func TestQueryAuditEventsPlatformAdminCanQueryOtherOrganization(t *testing.T) {
	svc, st := setupAuditService(t)
	now := time.Now().UTC()
	require.NoError(t, st.AuditEvents().Create(context.Background(), &store.AuditEvent{
		ID:             "event-other-org",
		OrganizationID: "org-002",
		Metadata:       map[string]string{},
		CreatedAt:      now.Add(-time.Minute),
	}))

	response, err := svc.QueryAuditEvents(auditContext(Principal{
		UserID: "user-platform",
		Roles:  []string{string(store.RolePlatformAdmin)},
		OrgID:  "org-001",
	}), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{
			OrganizationId: "org-002",
			TimeRange:      auditTimeRange(now.Add(-time.Hour), now),
		},
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetEvents(), 1)
	assert.Equal(t, "org-002", response.Msg.GetEvents()[0].GetActor().GetOrganizationId())
}

func TestQueryAuditEventsRejectsRangeTooLarge(t *testing.T) {
	svc, _ := setupAuditService(t)
	now := time.Now().UTC()
	_, err := svc.QueryAuditEvents(auditContext(Principal{
		UserID: "user-001",
		Roles:  []string{string(store.RoleReleaseAdmin)},
		OrgID:  "org-001",
	}), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{TimeRange: auditTimeRange(now.Add(-32*24*time.Hour), now)},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "range_too_large", errorReason(t, err))
}

func TestQueryAuditEventsPlatformAdminUsesExtendedRange(t *testing.T) {
	svc, _ := setupAuditService(t)
	now := time.Now().UTC()
	_, err := svc.QueryAuditEvents(auditContext(Principal{
		UserID: "user-platform",
		Roles:  []string{string(store.RolePlatformAdmin)},
		OrgID:  "org-001",
	}), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{TimeRange: auditTimeRange(now.Add(-365*24*time.Hour), now)},
	}))
	require.NoError(t, err)
}

func TestQueryAuditEventsRejectsInvalidCursor(t *testing.T) {
	svc, _ := setupAuditService(t)
	now := time.Now().UTC()
	_, err := svc.QueryAuditEvents(auditContext(Principal{
		UserID: "user-001",
		Roles:  []string{string(store.RoleReleaseAdmin)},
		OrgID:  "org-001",
	}), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter:     &auditv1.AuditQueryFilter{TimeRange: auditTimeRange(now.Add(-time.Hour), now)},
		Pagination: &commonv1.Pagination{PageToken: "not-a-cursor"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "invalid_cursor", errorReason(t, err))
}

func TestReasonMetadataIsAvailableOverHTTP(t *testing.T) {
	err := permissionDeniedError()
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "permission_denied", connectErr.Meta().Get("Reason-Code"))
}

func TestQueryAuditEventsCursorPaginationHasNoDuplicates(t *testing.T) {
	svc, st := setupAuditService(t)
	ctx := context.Background()
	createdAt := time.Date(2026, 7, 16, 12, 0, 0, 123, time.UTC)
	for index := range 5 {
		require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
			ID:             "event-00" + string(rune('1'+index)),
			OrganizationID: "org-001",
			Metadata:       map[string]string{},
			CreatedAt:      createdAt,
		}))
	}

	principalCtx := auditContext(Principal{
		UserID: "user-001",
		Roles:  []string{string(store.RoleReleaseAdmin)},
		OrgID:  "org-001",
	})
	seen := make(map[string]struct{})
	cursor := ""
	for range 3 {
		response, err := svc.QueryAuditEvents(principalCtx, connect.NewRequest(&auditv1.QueryAuditEventsRequest{
			Filter:     &auditv1.AuditQueryFilter{TimeRange: auditTimeRange(createdAt.Add(-time.Hour), createdAt.Add(time.Hour))},
			Pagination: &commonv1.Pagination{PageSize: 2, PageToken: cursor},
		}))
		require.NoError(t, err)
		for _, event := range response.Msg.GetEvents() {
			_, duplicate := seen[event.GetId()]
			assert.False(t, duplicate)
			seen[event.GetId()] = struct{}{}
		}
		cursor = response.Msg.GetPagination().GetNextPageToken()
	}
	assert.Len(t, seen, 5)
	assert.Empty(t, cursor)
}

func TestJWTInterceptorInjectsOrganizationPrincipal(t *testing.T) {
	jwtManager := auth.NewJWTManager([]byte("test-signing-key"), time.Hour, 24*time.Hour)
	token, _, err := jwtManager.GenerateAccessToken(
		"user-001",
		[]string{string(store.RoleReleaseAdmin)},
		"org-001",
	)
	require.NoError(t, err)

	var principal Principal
	interceptor := NewJWTInterceptor(jwtManager)
	next := interceptor(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		var ok bool
		principal, ok = principalFromContext(ctx)
		require.True(t, ok)
		return nil, nil
	})
	req := connect.NewRequest(&auditv1.QueryAuditEventsRequest{})
	req.Header().Set("Authorization", "Bearer "+token)
	_, err = next(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "user-001", principal.UserID)
	assert.Equal(t, "org-001", principal.OrgID)
	assert.Equal(t, []string{string(store.RoleReleaseAdmin)}, principal.Roles)
}

func TestQueryAuditEventsRedactsSensitiveMetadata(t *testing.T) {
	svc, st := setupAuditService(t)
	now := time.Now().UTC()
	require.NoError(t, st.AuditEvents().Create(context.Background(), &store.AuditEvent{
		ID:             "event-sensitive",
		OrganizationID: "org-001",
		ChangeSummary:  "token=plain-text-token",
		Metadata:       map[string]string{"api_token": "plain-text-token"},
		CreatedAt:      now.Add(-time.Minute),
	}))

	response, err := svc.QueryAuditEvents(auditContext(Principal{
		UserID: "user-001",
		Roles:  []string{string(store.RoleReleaseAdmin)},
		OrgID:  "org-001",
	}), connect.NewRequest(&auditv1.QueryAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{TimeRange: auditTimeRange(now.Add(-time.Hour), now)},
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GetEvents(), 1)
	assert.NotContains(t, response.Msg.GetEvents()[0].GetChangeSummary(), "plain-text-token")
	assert.Equal(t, "****REDACTED****", response.Msg.GetEvents()[0].GetMetadata()["api_token"])
}

func TestExportAuditEventsPersistsAuditRecord(t *testing.T) {
	svc, st := setupAuditService(t)
	now := time.Now().UTC()
	response, err := svc.ExportAuditEvents(auditContext(Principal{
		UserID: "user-001",
		Roles:  []string{string(store.RoleReleaseAdmin)},
		OrgID:  "org-001",
	}), connect.NewRequest(&auditv1.ExportAuditEventsRequest{
		Filter: &auditv1.AuditQueryFilter{TimeRange: auditTimeRange(now.Add(-time.Hour), now)},
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, response.Msg.GetExportId())

	page, err := st.AuditEvents().Query(context.Background(), store.AuditEventFilter{
		OrganizationID: "org-001",
		ResourceType:   "audit_export",
		ResourceID:     response.Msg.GetExportId(),
	}, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	assert.Equal(t, "create", page.Events[0].Action)
	assert.Equal(t, "accepted", page.Events[0].Status)
}

func errorReason(t *testing.T, err error) string {
	t.Helper()
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	return connectErr.Meta().Get("X-Reason-Code")
}
