package audit

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

// Service implements the audit collection Connect service.
type Service struct{ emitter Sink }

// NewService creates an audit collection service.
func NewService(emitter Sink) *Service { return &Service{emitter: emitter} }

// Emit accepts valid events and reports per-event rejection codes.
func (s *Service) Emit(_ context.Context, req *connect.Request[auditv1.EmitAuditRequest]) (*connect.Response[auditv1.EmitAuditResponse], error) {
	if req.Msg == nil || len(req.Msg.GetEvents()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s: events are required", ErrorInvalidEvent))
	}
	response := &auditv1.EmitAuditResponse{}
	for _, protoEvent := range req.Msg.GetEvents() {
		result := s.emitter.Emit(fromProto(protoEvent))
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

func fromProto(event *auditv1.AuditEvent) *store.AuditEvent {
	if event == nil {
		return nil
	}
	createdAt := time.Time{}
	if event.GetCreatedAt() != nil && event.GetCreatedAt().IsValid() {
		createdAt = event.GetCreatedAt().AsTime()
	}
	actor := event.GetActor()
	return &store.AuditEvent{
		ID: event.GetId(), ActorKind: actorKindFromProto(actor.GetKind()), ActorID: actor.GetId(), OrganizationID: actor.GetOrganizationId(), Role: actor.GetRole(),
		ResourceType: event.GetResourceType(), ResourceID: event.GetResourceId(), Action: event.GetAction(), Status: event.GetStatus(), DurationMs: event.GetDurationMs(),
		ChangeSummary: event.GetChangeSummary(), Metadata: event.GetMetadata(), CreatedAt: createdAt,
	}
}

func actorKindFromProto(kind auditv1.ActorKind) store.AuditActorKind {
	switch kind {
	case auditv1.ActorKind_ACTOR_KIND_ANONYMOUS:
		return store.AuditActorAnonymous
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

var _ auditv1connect.AuditServiceHandler = (*Service)(nil)
