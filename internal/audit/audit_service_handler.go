package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/contracts"
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
//
//nolint:dupl // This full handler intentionally mirrors the lightweight collector response contract.
func (h *auditServiceHandler) Emit(ctx context.Context, req *connect.Request[auditv1.EmitAuditRequest]) (*connect.Response[auditv1.EmitAuditResponse], error) {
	if req.Msg == nil || len(req.Msg.GetEvents()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s: events are required", ErrorInvalidEvent))
	}
	organizationID, err := principalOrganization(ctx)
	if err != nil {
		return nil, err
	}
	// TASK-095 AC-4: an emitted event may not claim another tenant. Events with
	// no organization stay allowed so infrastructure-level audit entries are not
	// blocked, but they cannot carry data into another organization either.
	for _, protoEvent := range req.Msg.GetEvents() {
		if eventOrg := protoEvent.GetActor().GetOrganizationId(); eventOrg != "" && eventOrg != organizationID {
			denied := connect.NewError(connect.CodePermissionDenied,
				errors.New("event organization does not match principal"))
			denied.Meta().Set("X-Reason-Code", "permission_denied")
			return nil, denied
		}
	}
	response := &auditv1.EmitAuditResponse{}
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
	msg := req.Msg
	filter := store.AuditEventFilter{}

	if f := msg.GetFilter(); f != nil {
		filter.OrganizationID = f.GetOrganizationId()
		filter.ResourceType = f.GetResourceType()
		filter.ResourceID = f.GetResourceId()
		filter.ActorID = f.GetActorId()
		filter.Action = f.GetAction()
		filter.Status = f.GetStatus()
		if tr := f.GetTimeRange(); tr != nil {
			if tr.GetStart() != nil && tr.GetStart().IsValid() {
				t := tr.GetStart().AsTime()
				filter.Since = &t
			}
			if tr.GetEnd() != nil && tr.GetEnd().IsValid() {
				t := tr.GetEnd().AsTime()
				filter.Until = &t
			}
		}
	}

	// TASK-095 AC-4: the principal's organization is the only readable scope;
	// a request that names another organization is denied rather than honored.
	scopedOrganization, err := authorizeOrganizationScope(ctx, filter.OrganizationID)
	if err != nil {
		return nil, err
	}
	filter.OrganizationID = scopedOrganization

	limit := int(contracts.NormalizePageSize(0))
	cursor := ""
	if p := msg.GetPagination(); p != nil {
		limit = int(contracts.NormalizePageSize(p.GetPageSize()))
		cursor = p.GetPageToken()
	}

	page, err := h.store.AuditEvents().Query(ctx, filter, cursor, limit)
	if err != nil {
		h.logger.Error("query audit events failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("query audit events: %w", err))
	}

	total, err := h.store.AuditEvents().Count(ctx, filter)
	if err != nil {
		h.logger.Warn("audit count failed", "error", err)
	}

	events := make([]*auditv1.AuditEvent, len(page.Events))
	for i, ev := range page.Events {
		events[i] = toProtoAuditEvent(ev)
	}

	return connect.NewResponse(&auditv1.QueryAuditEventsResponse{
		Events: events,
		Pagination: &commonv1.PaginationResponse{
			NextPageToken: page.NextCursor,
			TotalSize:     boundedAuditCount(total),
		},
	}), nil
}

// ExportAuditEvents creates an asynchronous export job and returns its ID.
func (h *auditServiceHandler) ExportAuditEvents(ctx context.Context, req *connect.Request[auditv1.ExportAuditEventsRequest]) (*connect.Response[auditv1.ExportAuditEventsResponse], error) {
	msg := req.Msg
	now := time.Now().UTC()

	// TASK-095 AC-4: an export is tenant-scoped; the organization recorded on
	// the job comes from the principal, never from the request.
	scopedOrganization, err := authorizeOrganizationScope(ctx, msg.GetFilter().GetOrganizationId())
	if err != nil {
		return nil, err
	}

	var since, until time.Time
	if f := msg.GetFilter(); f != nil {
		if tr := f.GetTimeRange(); tr != nil {
			if tr.GetStart() != nil && tr.GetStart().IsValid() {
				since = tr.GetStart().AsTime()
			}
			if tr.GetEnd() != nil && tr.GetEnd().IsValid() {
				until = tr.GetEnd().AsTime()
			}
		}
	}
	if since.IsZero() {
		since = now.AddDate(0, 0, -30)
	}
	if until.IsZero() {
		until = now
	}

	export := &store.AuditExport{
		ID:             uuid.New().String(),
		OrganizationID: scopedOrganization,
		Since:          since,
		Until:          until,
		Status:         "pending",
		CreatedAt:      now,
	}

	event := &store.AuditEvent{
		ID:             uuid.New().String(),
		ActorKind:      store.AuditActorSystem,
		ActorID:        "system",
		OrganizationID: scopedOrganization,
		ResourceType:   "audit_export",
		ResourceID:     export.ID,
		Action:         "export.created",
		Status:         "success",
		CreatedAt:      now,
	}

	if err := h.store.AuditExports().CreateWithEvent(ctx, export, event); err != nil {
		h.logger.Error("create audit export failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create audit export: %w", err))
	}

	return connect.NewResponse(&auditv1.ExportAuditEventsResponse{
		ExportId: export.ID,
		Status:   export.Status,
	}), nil
}

func toProtoAuditEvent(ev *store.AuditEvent) *auditv1.AuditEvent {
	if ev == nil {
		return nil
	}
	return &auditv1.AuditEvent{
		Id:         ev.ID,
		Action:     ev.Action,
		Status:     ev.Status,
		DurationMs: ev.DurationMs,
	}
}

func boundedAuditCount(total int64) int32 {
	if total <= 0 {
		return 0
	}
	const maxInt32 = int64(1<<31 - 1)
	if total > maxInt32 {
		return int32(maxInt32)
	}
	return int32(total) //nolint:gosec // Value is explicitly bounded to the int32 range.
}
