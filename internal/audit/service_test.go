package audit

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

type sinkFunc func(eventID string) Result

func (fn sinkFunc) Emit(event *store.AuditEvent) Result { return fn(event.ID) }

func TestService_EmitReturnsAcceptedIDsAndRejections(t *testing.T) {
	service := NewService(sinkFunc(func(eventID string) Result {
		if eventID == "reject" {
			return Result{EventID: eventID, Code: ErrorBufferFull}
		}
		return Result{EventID: eventID, Accepted: true}
	}))
	response, err := service.Emit(t.Context(), connect.NewRequest(&auditv1.EmitRequest{Events: []*auditv1.AuditEvent{
		{Id: "accepted", Actor: &auditv1.AuditActor{Kind: auditv1.ActorKind_ACTOR_KIND_API_KEY, Id: "key-1"}, ResourceType: "release", Action: "update", Status: "accepted"},
		{Id: "reject", Actor: &auditv1.AuditActor{Kind: auditv1.ActorKind_ACTOR_KIND_SERVICE, Id: "svc-1"}, ResourceType: "release", Action: "update", Status: "accepted"},
	}}))
	require.NoError(t, err)
	assert.Equal(t, int32(1), response.Msg.Accepted)
	assert.Equal(t, []string{"accepted"}, response.Msg.GetAuditEventIds())
	assert.Equal(t, []string{string(ErrorBufferFull)}, response.Msg.GetRejectionCodes())
}
