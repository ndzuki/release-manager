package audit

import (
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// NewEvent builds a standard event for business-service write paths.
func NewEvent(actorKind store.AuditActorKind, actorID, organizationID, role, resourceType, resourceID, action, status, summary string, metadata map[string]string) *store.AuditEvent {
	return &store.AuditEvent{
		ActorKind:      actorKind,
		ActorID:        actorID,
		OrganizationID: organizationID,
		Role:           role,
		ResourceType:   resourceType,
		ResourceID:     resourceID,
		Action:         action,
		Status:         status,
		ChangeSummary:  summary,
		Metadata:       metadata,
		CreatedAt:      time.Now().UTC(),
	}
}
