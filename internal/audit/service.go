package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

const (
	defaultAuditPageSize = 50
	maxAuditPageSize     = 200
	maxAuditRange        = 31 * 24 * time.Hour
	maxPlatformRange     = 366 * 24 * time.Hour
	maxPaginationTotal   = int64(1<<31 - 1)
)

type eventEmitter interface {
	Emit(*store.AuditEvent) bool
}

type auditStore interface {
	AuditEvents() store.AuditEventStore
	AuditExports() store.AuditExportStore
}

// ServiceHandler implements organization-scoped audit queries and exports.
type ServiceHandler struct {
	store   auditStore
	emitter eventEmitter
	logger  *slog.Logger
}

// NewAuditServiceHandler creates the Audit Connect handler.
func NewAuditServiceHandler(st auditStore, emitter eventEmitter, logger *slog.Logger) *ServiceHandler {
	return &ServiceHandler{store: st, emitter: emitter, logger: logger}
}

var _ auditv1connect.AuditServiceHandler = (*ServiceHandler)(nil)

// Emit accepts audit events for asynchronous persistence.
func (s *ServiceHandler) Emit(
	_ context.Context,
	req *connect.Request[auditv1.EmitAuditRequest],
) (*connect.Response[auditv1.EmitAuditResponse], error) {
	accepted := 0
	rejected := 0
	for _, event := range req.Msg.GetEvents() {
		if s.emitter.Emit(sanitizeAuditEvent(fromProtoAuditEvent(event))) {
			accepted++
		} else {
			rejected++
		}
	}
	return connect.NewResponse(&auditv1.EmitAuditResponse{
		Accepted: int32(accepted),
		Rejected: int32(rejected),
	}), nil
}

// QueryAuditEvents returns stable cursor-paginated audit events.
func (s *ServiceHandler) QueryAuditEvents(
	ctx context.Context,
	req *connect.Request[auditv1.QueryAuditEventsRequest],
) (*connect.Response[auditv1.QueryAuditEventsResponse], error) {
	principal, ok := principalFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("user not authenticated"))
	}

	filter, err := auditFilter(req.Msg.GetFilter(), false)
	if err != nil {
		return nil, invalidAuditError(err, "invalid_filter")
	}
	if err := authorizeFilter(&filter, principal); err != nil {
		return nil, err
	}

	limit, cursor, err := auditPagination(req.Msg.GetPagination())
	if err != nil {
		return nil, invalidAuditError(err, "invalid_page_size")
	}

	page, err := s.store.AuditEvents().Query(ctx, filter, cursor, limit)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCursor) {
			return nil, invalidAuditError(errors.New("invalid_cursor"), "invalid_cursor")
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("query audit events: %w", err))
	}
	total, err := s.store.AuditEvents().Count(ctx, filter)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("count audit events: %w", err))
	}
	if total > maxPaginationTotal {
		total = maxPaginationTotal
	}

	response := &auditv1.QueryAuditEventsResponse{
		Events: make([]*auditv1.AuditEvent, 0, len(page.Events)),
		Pagination: &commonv1.PaginationResponse{
			NextPageToken: page.NextCursor,
			TotalSize:     int32(total),
		},
	}
	for _, event := range page.Events {
		response.Events = append(response.Events, toProtoAuditEvent(sanitizeAuditEvent(event)))
	}
	return connect.NewResponse(response), nil
}

// ExportAuditEvents creates an asynchronous export job and audits the request.
func (s *ServiceHandler) ExportAuditEvents(
	ctx context.Context,
	req *connect.Request[auditv1.ExportAuditEventsRequest],
) (*connect.Response[auditv1.ExportAuditEventsResponse], error) {
	principal, ok := principalFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("user not authenticated"))
	}

	filter, err := auditFilter(req.Msg.GetFilter(), true)
	if err != nil {
		return nil, invalidAuditError(err, "invalid_filter")
	}
	if err := authorizeFilter(&filter, principal); err != nil {
		return nil, err
	}

	export := &store.AuditExport{
		ID:             uuid.NewString(),
		OrganizationID: filter.OrganizationID,
		Since:          *filter.Since,
		Until:          *filter.Until,
		Status:         "pending",
	}

	auditEvent := &store.AuditEvent{
		ID:             uuid.NewString(),
		ActorKind:      store.AuditActorUser,
		ActorID:        principal.UserID,
		OrganizationID: principal.OrgID,
		Role:           primaryRole(principal.Roles),
		ResourceType:   "audit_export",
		ResourceID:     export.ID,
		Action:         "create",
		Status:         "accepted",
		ChangeSummary:  "audit export requested",
		Metadata: map[string]string{
			"since": filter.Since.UTC().Format(time.RFC3339Nano),
			"until": filter.Until.UTC().Format(time.RFC3339Nano),
		},
	}
	if err := s.store.AuditExports().CreateWithEvent(ctx, export, auditEvent); err != nil {
		s.logger.Error("create audit export failed", "error", err)
		return nil, reasonError(connect.CodeUnavailable, "export_unavailable")
	}

	s.logger.Info("audit export accepted", "export_id", export.ID, "organization_id", export.OrganizationID)
	return connect.NewResponse(&auditv1.ExportAuditEventsResponse{
		ExportId: export.ID,
		Status:   export.Status,
	}), nil
}

func auditPagination(pagination *commonv1.Pagination) (limit int, cursor string, err error) {
	if pagination == nil {
		return defaultAuditPageSize, "", nil
	}
	limit = int(pagination.GetPageSize())
	if limit == 0 {
		limit = defaultAuditPageSize
	}
	if limit < 1 || limit > maxAuditPageSize {
		return 0, "", errors.New("page_size must be between 1 and 200")
	}
	return limit, pagination.GetPageToken(), nil
}

func auditFilter(input *auditv1.AuditQueryFilter, requireRange bool) (store.AuditEventFilter, error) {
	filter := store.AuditEventFilter{}
	if input == nil {
		if requireRange {
			return filter, errors.New("time range is required")
		}
		return filter, nil
	}
	filter.OrganizationID = input.GetOrganizationId()
	filter.ResourceType = input.GetResourceType()
	filter.ResourceID = input.GetResourceId()
	filter.ActorID = input.GetActorId()
	filter.Action = input.GetAction()
	filter.Status = input.GetStatus()

	timeRange := input.GetTimeRange()
	if timeRange == nil {
		if requireRange {
			return filter, errors.New("time range is required")
		}
		return filter, nil
	}
	if timeRange.GetStart() == nil || timeRange.GetEnd() == nil {
		return filter, errors.New("time range start and end are required")
	}
	if err := timeRange.GetStart().CheckValid(); err != nil {
		return filter, errors.New("invalid time range start")
	}
	if err := timeRange.GetEnd().CheckValid(); err != nil {
		return filter, errors.New("invalid time range end")
	}
	since := timeRange.GetStart().AsTime()
	until := timeRange.GetEnd().AsTime()
	if !since.Before(until) {
		return filter, errors.New("time range start must be before end")
	}
	filter.Since = &since
	filter.Until = &until
	return filter, nil
}

func authorizeFilter(filter *store.AuditEventFilter, principal Principal) error {
	platformAdmin := hasRole(principal.Roles, string(store.RolePlatformAdmin))
	if !platformAdmin {
		if principal.OrgID == "" {
			return permissionDeniedError()
		}
		if filter.OrganizationID != "" && filter.OrganizationID != principal.OrgID {
			return permissionDeniedError()
		}
		filter.OrganizationID = principal.OrgID
	}

	if filter.Since == nil || filter.Until == nil {
		return nil
	}
	maxRange := maxAuditRange
	if platformAdmin {
		maxRange = maxPlatformRange
	}
	if filter.Until.Sub(*filter.Since) > maxRange {
		return rangeTooLargeError()
	}
	return nil
}

func hasRole(roles []string, role string) bool {
	for _, candidate := range roles {
		if candidate == role {
			return true
		}
	}
	return false
}

func primaryRole(roles []string) string {
	if hasRole(roles, string(store.RolePlatformAdmin)) {
		return string(store.RolePlatformAdmin)
	}
	if len(roles) > 0 {
		return roles[0]
	}
	return ""
}

func permissionDeniedError() error {
	return reasonError(connect.CodePermissionDenied, "permission_denied")
}

func rangeTooLargeError() error {
	return reasonError(connect.CodeInvalidArgument, "range_too_large")
}

func invalidAuditError(err error, reason string) error {
	result := connect.NewError(connect.CodeInvalidArgument, err)
	setReasonMetadata(result, reason)
	return result
}

func reasonError(code connect.Code, reason string) error {
	result := connect.NewError(code, errors.New(reason))
	setReasonMetadata(result, reason)
	return result
}

func setReasonMetadata(result *connect.Error, reason string) {
	result.Meta().Set("X-Reason-Code", reason)
	result.Meta().Set("Reason-Code", reason)
}

func sanitizeAuditEvent(event *store.AuditEvent) *store.AuditEvent {
	if event == nil {
		return &store.AuditEvent{}
	}
	copyEvent := *event
	copyEvent.ChangeSummary, _ = Sanitize(event.ChangeSummary)
	copyEvent.Metadata = make(map[string]string, len(event.Metadata))
	for key, value := range event.Metadata {
		value, _ = Sanitize(value)
		copyEvent.Metadata[key] = RedactSensitive(key, value)
	}
	return &copyEvent
}

func fromProtoAuditEvent(event *auditv1.AuditEvent) *store.AuditEvent {
	if event == nil {
		return &store.AuditEvent{}
	}
	actor := event.GetActor()
	result := &store.AuditEvent{
		ID:            event.GetId(),
		ResourceType:  event.GetResourceType(),
		ResourceID:    event.GetResourceId(),
		Action:        event.GetAction(),
		Status:        event.GetStatus(),
		DurationMs:    event.GetDurationMs(),
		ChangeSummary: event.GetChangeSummary(),
		Metadata:      event.GetMetadata(),
	}
	if actor != nil {
		result.ActorKind = actorKindFromProto(actor.GetKind())
		result.ActorID = actor.GetId()
		result.OrganizationID = actor.GetOrganizationId()
		result.Role = actor.GetRole()
	}
	if event.GetCreatedAt() != nil {
		result.CreatedAt = event.GetCreatedAt().AsTime()
	}
	return result
}

func toProtoAuditEvent(event *store.AuditEvent) *auditv1.AuditEvent {
	return &auditv1.AuditEvent{
		Id: event.ID,
		Actor: &auditv1.AuditActor{
			Kind:           actorKindToProto(event.ActorKind),
			Id:             event.ActorID,
			OrganizationId: event.OrganizationID,
			Role:           event.Role,
		},
		ResourceType:  event.ResourceType,
		ResourceId:    event.ResourceID,
		Action:        event.Action,
		Status:        event.Status,
		DurationMs:    event.DurationMs,
		ChangeSummary: event.ChangeSummary,
		CreatedAt:     timestamppb.New(event.CreatedAt),
		Metadata:      event.Metadata,
	}
}

func actorKindFromProto(kind auditv1.ActorKind) store.AuditActorKind {
	switch kind {
	case auditv1.ActorKind_ACTOR_KIND_USER:
		return store.AuditActorUser
	case auditv1.ActorKind_ACTOR_KIND_SERVICE:
		return store.AuditActorService
	case auditv1.ActorKind_ACTOR_KIND_API_KEY:
		return store.AuditActorAPIKey
	case auditv1.ActorKind_ACTOR_KIND_SYSTEM:
		return store.AuditActorSystem
	default:
		return ""
	}
}

func actorKindToProto(kind store.AuditActorKind) auditv1.ActorKind {
	switch kind {
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
