package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// auditServiceHandler is the full AuditServiceHandler implementation that
// delegates Emit to an emitter and QueryAuditEvents / ExportAuditEvents to the store.
type auditServiceHandler struct {
	auditv1connect.UnimplementedAuditServiceHandler
	store   store.Store
	emitter Sink
	logger  *slog.Logger
}

// NewAuditServiceHandler creates a handler that satisfies the full
// audit.v1.AuditServiceHandler interface.
func NewAuditServiceHandler(st store.Store, emitter Sink, logger *slog.Logger) auditv1connect.AuditServiceHandler {
	return &auditServiceHandler{store: st, emitter: emitter, logger: logger}
}

// Emit delegates to the underlying emitter.
func (h *auditServiceHandler) Emit(ctx context.Context, req *connect.Request[auditv1.EmitRequest]) (*connect.Response[auditv1.EmitResponse], error) {
	if req.Msg == nil || len(req.Msg.GetEvents()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s: events are required", ErrorInvalidEvent))
	}
	response := &auditv1.EmitResponse{}
	for _, protoEvent := range req.Msg.GetEvents() {
		result := h.emitter.Emit(fromProto(protoEvent))
		if result.Accepted {
			response.Accepted++
			response.AuditEventIds = append(response.AuditEventIds, result.EventID)
			continue
		}
		response.Rejected++
		response.RejectionCodes = append(response.RejectionCodes, string(result.Code))
	}
	return connect.NewResponse(response), nil
}

// QueryAuditEvents delegates to the store.
func (h *auditServiceHandler) QueryAuditEvents(ctx context.Context, req *connect.Request[auditv1.QueryAuditEventsRequest]) (*connect.Response[auditv1.QueryAuditEventsResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	filter, err := buildAuditFilter(req.Msg.GetFilter(), principal, hasRole(principal, store.RolePlatformAdmin))
	if err != nil {
		return nil, err
	}

	limit := 20
	cursor := ""
	if pagination := req.Msg.GetPagination(); pagination != nil {
		if pagination.GetPageSize() > 0 {
			limit = min(int(pagination.GetPageSize()), 100)
		}
		cursor = pagination.GetPageToken()
	}

	page, err := h.store.AuditEvents().Query(ctx, filter, cursor, limit)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCursor) {
			return nil, auditError(connect.CodeInvalidArgument, "invalid_cursor", "page cursor is invalid", 0, "")
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("query audit events: %w", err))
	}
	total, err := h.store.AuditEvents().Count(ctx, filter)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("count audit events: %w", err))
	}

	canSeeActorDetails := hasRole(principal, store.RolePlatformAdmin) || hasRole(principal, store.RoleReleaseAdmin)
	events := make([]*auditv1.AuditEvent, len(page.Events))
	for i, event := range page.Events {
		events[i] = toProtoAuditEvent(event, canSeeActorDetails)
	}
	return connect.NewResponse(&auditv1.QueryAuditEventsResponse{
		Events: events,
		Pagination: &commonv1.PaginationResponse{
			NextPageToken: page.NextCursor,
			TotalSize:     int32(total),
		},
	}), nil
}

// ExportAuditEvents creates an asynchronous export job and returns its ID.
func (h *auditServiceHandler) ExportAuditEvents(ctx context.Context, req *connect.Request[auditv1.ExportAuditEventsRequest]) (*connect.Response[auditv1.ExportAuditEventsResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	filter, err := buildAuditFilter(req.Msg.GetFilter(), principal, hasRole(principal, store.RolePlatformAdmin))
	if err != nil {
		return nil, err
	}
	if format := req.Msg.GetFormat(); format != auditv1.ExportFormat_EXPORT_FORMAT_UNSPECIFIED && format != auditv1.ExportFormat_EXPORT_FORMAT_CSV {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported export format"))
	}
	if maxRows := req.Msg.GetMaxRows(); maxRows > 10000 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("max_rows exceeds 10000"))
	}

	now := time.Now().UTC()
	export := &store.AuditExport{
		ID:             uuid.NewString(),
		OrganizationID: filter.OrganizationID,
		Since:          dereferenceTime(filter.Since, now.Add(-24*time.Hour)),
		Until:          dereferenceTime(filter.Until, now),
		Status:         "pending",
		CreatedAt:      now,
	}
	event := &store.AuditEvent{
		ID:             uuid.NewString(),
		ActorKind:      store.AuditActorUser,
		ActorID:        principal.UserID,
		OrganizationID: filter.OrganizationID,
		Role:           firstRole(principal.Roles),
		ResourceType:   "audit_export",
		ResourceID:     export.ID,
		Action:         "export",
		Status:         "success",
		CreatedAt:      now,
	}
	if err := h.store.AuditExports().CreateWithEvent(ctx, export, event); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("create audit export: %w", err))
	}
	return connect.NewResponse(&auditv1.ExportAuditEventsResponse{
		ExportId:  export.ID,
		TaskId:    export.ID,
		Status:    export.Status,
		CreatedAt: timestamppb.New(export.CreatedAt),
	}), nil
}

func (h *auditServiceHandler) GetAuditExportStatus(ctx context.Context, req *connect.Request[auditv1.GetAuditExportStatusRequest]) (*connect.Response[auditv1.GetAuditExportStatusResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	export, err := h.store.AuditExports().Get(ctx, req.Msg.GetTaskId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("audit export not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get audit export: %w", err))
	}
	if export.OrganizationID != principal.OrgID && !hasRole(principal, store.RolePlatformAdmin) {
		return nil, auditError(connect.CodePermissionDenied, "permission_denied", "organization access denied", 0, "")
	}
	response := &auditv1.GetAuditExportStatusResponse{
		TaskId:       export.ID,
		Status:       export.Status,
		DownloadUrl:  export.DownloadURL,
		ErrorMessage: export.ErrorMessage,
		CreatedAt:    timestamppb.New(export.CreatedAt),
	}
	if export.CompletedAt != nil {
		response.CompletedAt = timestamppb.New(*export.CompletedAt)
	}
	return connect.NewResponse(response), nil
}

func buildAuditFilter(input *auditv1.AuditQueryFilter, principal Principal, canCrossOrganizations bool) (store.AuditEventFilter, error) {
	filter := store.AuditEventFilter{OrganizationID: principal.OrgID}
	if input == nil {
		return filter, nil
	}
	if requestedOrg := strings.TrimSpace(input.GetOrganizationId()); requestedOrg != "" {
		if requestedOrg != principal.OrgID && !canCrossOrganizations {
			return filter, auditError(connect.CodePermissionDenied, "permission_denied", "organization access denied", 0, "")
		}
		filter.OrganizationID = requestedOrg
	}
	filter.ResourceType = resourceTypeValue(input)
	filter.ResourceID = strings.TrimSpace(input.GetResourceId())
	filter.ActorID = strings.TrimSpace(input.GetActorId())
	filter.Actions = actionValues(input)
	filter.Statuses = statusValues(input)
	filter.OperationID = strings.TrimSpace(input.GetOperationId())
	if timeRange := input.GetTimeRange(); timeRange != nil {
		if start := timeRange.GetStart(); start != nil && start.IsValid() {
			value := start.AsTime().UTC()
			filter.Since = &value
		}
		if end := timeRange.GetEnd(); end != nil && end.IsValid() {
			value := end.AsTime().UTC()
			filter.Until = &value
		}
	}
	if filter.Since != nil && filter.Until != nil {
		if filter.Since.After(*filter.Until) {
			return filter, connect.NewError(connect.CodeInvalidArgument, errors.New("from must not be after to"))
		}
		if filter.Until.Sub(*filter.Since) > 30*24*time.Hour {
			return filter, auditError(connect.CodeInvalidArgument, "range_too_large", "audit range exceeds 30 days", 30, "")
		}
	}
	return filter, nil
}

func toProtoAuditEvent(event *store.AuditEvent, canSeeActorDetails bool) *auditv1.AuditEvent {
	if event == nil {
		return nil
	}
	actorID := event.ActorID
	role := event.Role
	displayName := event.ActorName
	if !canSeeActorDetails {
		actorID = maskActorID(actorID)
		role = ""
		displayName = ""
	}
	return &auditv1.AuditEvent{
		Id: event.ID,
		Actor: &auditv1.AuditActor{
			Kind:           actorKindToProto(event.ActorKind),
			Id:             actorID,
			OrganizationId: event.OrganizationID,
			Role:           role,
			DisplayName:    displayName,
		},
		ResourceType:  event.ResourceType,
		ResourceId:    event.ResourceID,
		Action:        event.Action,
		Status:        event.Status,
		OperationId:   event.OperationID,
		RequestId:     event.RequestID,
		DurationMs:    event.DurationMs,
		ChangeSummary: event.ChangeSummary,
		Metadata:      event.Metadata,
		CreatedAt:     timestamppb.New(event.CreatedAt),
	}
}

func requirePrincipal(ctx context.Context) (Principal, error) {
	principal, ok := principalFromContext(ctx)
	if !ok || principal.UserID == "" || principal.OrgID == "" {
		return Principal{}, connect.NewError(connect.CodeUnauthenticated, errors.New("authenticated principal is required"))
	}
	return principal, nil
}

func hasRole(principal Principal, role store.Role) bool {
	for _, candidate := range principal.Roles {
		if candidate == string(role) {
			return true
		}
	}
	return false
}

func firstRole(roles []string) string {
	if len(roles) == 0 {
		return ""
	}
	return roles[0]
}

func dereferenceTime(value *time.Time, fallback time.Time) time.Time {
	if value == nil {
		return fallback
	}
	return *value
}

func maskActorID(actorID string) string {
	if len(actorID) <= 4 {
		return "***"
	}
	return actorID[:1] + "***" + actorID[len(actorID)-3:]
}

func resourceTypeValue(filter *auditv1.AuditQueryFilter) string {
	if filter.GetResourceTypeEnum() != auditv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
		return strings.ToLower(strings.TrimPrefix(filter.GetResourceTypeEnum().String(), "RESOURCE_TYPE_"))
	}
	return strings.TrimSpace(filter.GetResourceType())
}

func actionValues(filter *auditv1.AuditQueryFilter) []string {
	values := make([]string, 0, len(filter.GetActions())+1)
	for _, action := range filter.GetActions() {
		if action != auditv1.ActionType_ACTION_TYPE_UNSPECIFIED {
			values = append(values, strings.ToLower(strings.TrimPrefix(action.String(), "ACTION_")))
		}
	}
	if legacy := strings.TrimSpace(filter.GetAction()); legacy != "" {
		values = append(values, legacy)
	}
	return values
}

func statusValues(filter *auditv1.AuditQueryFilter) []string {
	values := make([]string, 0, len(filter.GetStatuses())+1)
	for _, status := range filter.GetStatuses() {
		if status != auditv1.StatusType_STATUS_TYPE_UNSPECIFIED {
			values = append(values, strings.ToLower(strings.TrimPrefix(status.String(), "STATUS_")))
		}
	}
	if legacy := strings.TrimSpace(filter.GetStatus()); legacy != "" {
		values = append(values, legacy)
	}
	return values
}

func actorKindToProto(kind store.AuditActorKind) auditv1.ActorKind {
	switch kind {
	case store.AuditActorAnonymous:
		return auditv1.ActorKind_ACTOR_KIND_ANONYMOUS
	case store.AuditActorUser:
		return auditv1.ActorKind_ACTOR_KIND_USER
	case store.AuditActorService:
		return auditv1.ActorKind_ACTOR_KIND_SERVICE
	case store.AuditActorAPIKey:
		return auditv1.ActorKind_ACTOR_KIND_API_KEY
	case store.AuditActorSystem:
		return auditv1.ActorKind_ACTOR_KIND_SYSTEM
	default:
		return auditv1.ActorKind_ACTOR_KIND_UNSPECIFIED
	}
}

func auditError(code connect.Code, reason, message string, maxRangeDays int32, retryAfter string) error {
	connectErr := connect.NewError(code, errors.New(message))
	detail, err := connect.NewErrorDetail(&auditv1.AuditErrorDetail{
		Reason:       reason,
		MaxRangeDays: maxRangeDays,
		RetryAfter:   retryAfter,
	})
	if err == nil {
		connectErr.AddDetail(detail)
	}
	connectErr.Meta().Set("X-Reason-Code", reason)
	return connectErr
}
