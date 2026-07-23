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
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestAuditService_QueryEnforcesOrganizationAndRange(t *testing.T) {
	service := newAuditHandlerTestService(t)
	principal := Principal{UserID: "user-1", OrgID: "org-1", Roles: []string{string(store.RoleReleaseAdmin)}}
	ctx := context.WithValue(t.Context(), principalContextKey{}, principal)

	tests := []struct {
		name       string
		filter     *auditv1.AuditQueryFilter
		wantCode   connect.Code
		wantReason string
	}{
		{
			name:       "cross organization rejected",
			filter:     &auditv1.AuditQueryFilter{OrganizationId: "org-2"},
			wantCode:   connect.CodePermissionDenied,
			wantReason: "permission_denied",
		},
		{
			name: "range over 30 days rejected",
			filter: &auditv1.AuditQueryFilter{TimeRange: &commonv1.TimestampRange{
				Start: timestamppb.New(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)),
				End:   timestamppb.New(time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)),
			}},
			wantCode:   connect.CodeInvalidArgument,
			wantReason: "range_too_large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.QueryAuditEvents(ctx, connect.NewRequest(&auditv1.QueryAuditEventsRequest{Filter: tt.filter}))
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Equal(t, tt.wantReason, connectErr.Meta().Get("X-Reason-Code"))
		})
	}
}

func TestAuditService_QueryRedactsActorForMember(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	createdAt := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	require.NoError(t, st.AuditEvents().Create(t.Context(), &store.AuditEvent{
		ID:             "audit-1",
		ActorKind:      store.AuditActorUser,
		ActorID:        "user-123456",
		ActorName:      "Alice Admin",
		OrganizationID: "org-1",
		Role:           string(store.RoleReleaseAdmin),
		ResourceType:   "OPERATION",
		ResourceID:     "operation-1",
		Action:         "UPGRADE",
		Status:         "SUCCESS",
		OperationID:    "operation-1",
		RequestID:      "request-1",
		CreatedAt:      createdAt,
		Metadata:       map[string]string{},
	}))
	service := NewAuditServiceHandler(st, sinkFunc(func(eventID string) Result {
		return Result{EventID: eventID, Accepted: true}
	}), slog.New(slog.DiscardHandler))

	tests := []struct {
		name            string
		roles           []string
		wantActorID     string
		wantRole        string
		wantDisplayName string
	}{
		{
			name:        "viewer receives masked actor",
			roles:       []string{string(store.RoleViewer)},
			wantActorID: "u***456",
		},
		{
			name:            "release admin receives full actor",
			roles:           []string{string(store.RoleReleaseAdmin)},
			wantActorID:     "user-123456",
			wantRole:        string(store.RoleReleaseAdmin),
			wantDisplayName: "Alice Admin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.WithValue(t.Context(), principalContextKey{}, Principal{
				UserID: "caller-1", OrgID: "org-1", Roles: tt.roles,
			})
			response, err := service.QueryAuditEvents(ctx, connect.NewRequest(&auditv1.QueryAuditEventsRequest{}))
			require.NoError(t, err)
			require.Len(t, response.Msg.GetEvents(), 1)
			actor := response.Msg.GetEvents()[0].GetActor()
			require.NotNil(t, actor)
			assert.Equal(t, tt.wantActorID, actor.GetId())
			assert.Equal(t, tt.wantRole, actor.GetRole())
			assert.Equal(t, tt.wantDisplayName, actor.GetDisplayName())
		})
	}
}

func TestAuditService_ExportLifecycle(t *testing.T) {
	service := newAuditHandlerTestService(t)
	ctx := context.WithValue(t.Context(), principalContextKey{}, Principal{
		UserID: "user-1", OrgID: "org-1", Roles: []string{string(store.RoleReleaseAdmin)},
	})

	created, err := service.ExportAuditEvents(ctx, connect.NewRequest(&auditv1.ExportAuditEventsRequest{
		Format:  auditv1.ExportFormat_EXPORT_FORMAT_CSV,
		MaxRows: 10000,
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Msg.GetTaskId())
	assert.Equal(t, "pending", created.Msg.GetStatus())

	status, err := service.GetAuditExportStatus(ctx, connect.NewRequest(&auditv1.GetAuditExportStatusRequest{
		TaskId: created.Msg.GetTaskId(),
	}))
	require.NoError(t, err)
	assert.Equal(t, created.Msg.GetTaskId(), status.Msg.GetTaskId())
	assert.Equal(t, "pending", status.Msg.GetStatus())
}

func newAuditHandlerTestService(t *testing.T) auditv1connectHandler {
	t.Helper()
	st := sqlitestore.OpenTest(t)
	return NewAuditServiceHandler(st, sinkFunc(func(eventID string) Result {
		return Result{EventID: eventID, Accepted: true}
	}), slog.New(slog.DiscardHandler))
}

type auditv1connectHandler interface {
	QueryAuditEvents(context.Context, *connect.Request[auditv1.QueryAuditEventsRequest]) (*connect.Response[auditv1.QueryAuditEventsResponse], error)
	ExportAuditEvents(context.Context, *connect.Request[auditv1.ExportAuditEventsRequest]) (*connect.Response[auditv1.ExportAuditEventsResponse], error)
	GetAuditExportStatus(context.Context, *connect.Request[auditv1.GetAuditExportStatusRequest]) (*connect.Response[auditv1.GetAuditExportStatusResponse], error)
}
