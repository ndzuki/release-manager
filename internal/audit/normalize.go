package audit

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ndzuki/release-manager/internal/store"
)

// Normalize validates and copies an event before it crosses the emitter boundary.
func Normalize(event *store.AuditEvent) (*store.AuditEvent, error) {
	if event == nil {
		return nil, fmt.Errorf("%w: event is nil", ErrInvalidEvent)
	}
	copyEvent := *event
	normalizeIdentity(&copyEvent)
	if err := validateActor(&copyEvent); err != nil {
		return nil, err
	}
	if strings.TrimSpace(copyEvent.ResourceType) == "" || strings.TrimSpace(copyEvent.Action) == "" || strings.TrimSpace(copyEvent.Status) == "" {
		return nil, fmt.Errorf("%w: resource type, action, and status are required", ErrInvalidEvent)
	}
	copyEvent.ChangeSummary, _ = Sanitize(copyEvent.ChangeSummary)
	copyEvent.Metadata = sanitizeMetadata(event.Metadata)
	return &copyEvent, nil
}

func normalizeIdentity(event *store.AuditEvent) {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	} else {
		event.CreatedAt = event.CreatedAt.UTC()
	}
}

func validateActor(event *store.AuditEvent) error {
	switch event.ActorKind {
	case store.AuditActorAnonymous, store.AuditActorSystem:
		return nil
	case store.AuditActorUser, store.AuditActorAPIKey, store.AuditActorService:
		if strings.TrimSpace(event.ActorID) == "" {
			return fmt.Errorf("%w: actor id is required for %s", ErrInvalidEvent, event.ActorKind)
		}
		return nil
	case "":
		return fmt.Errorf("%w: actor kind is required", ErrInvalidEvent)
	default:
		return fmt.Errorf("%w: unsupported actor kind %q", ErrInvalidEvent, event.ActorKind)
	}
}

func sanitizeMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	result := make(map[string]string, len(metadata))
	for key, value := range metadata {
		value = RedactSensitive(key, value)
		result[key], _ = Sanitize(value)
	}
	return result
}
