package audit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

func sampleEvent() *store.AuditEvent {
	return &store.AuditEvent{
		ID:             "ev-1",
		ActorKind:      store.AuditActorUser,
		ActorID:        "user-abcdef123456",
		OrganizationID: "org-1",
		Role:           "release_admin",
		ResourceType:   "release_definition",
		ResourceID:     "def-1",
		Action:         "create",
		Status:         "success",
		DurationMs:     42,
		ChangeSummary:  "added a value",
		Metadata:       map[string]string{"k": "v"},
		CreatedAt:      time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
	}
}

// AC-059-05: a caller without platform_admin or release_admin sees only a masked
// actor id and no role.
func TestToProtoAuditEvent_MasksActorForOrdinaryMembers(t *testing.T) {
	out := toProtoAuditEvent(sampleEvent(), false)

	require.NotNil(t, out.GetActor())
	assert.Equal(t, "u***456", out.GetActor().GetId(), "the actor id must be masked")
	assert.Empty(t, out.GetActor().GetRole(), "the role must not be disclosed")
	assert.Equal(t, "org-1", out.GetActor().GetOrganizationId())
	assert.Equal(t, "user-abcdef123456", sampleEvent().ActorID, "the stored event must not be mutated")
}

func TestToProtoAuditEvent_KeepsActorForPrivilegedCallers(t *testing.T) {
	out := toProtoAuditEvent(sampleEvent(), true)

	require.NotNil(t, out.GetActor())
	assert.Equal(t, "user-abcdef123456", out.GetActor().GetId())
	assert.Equal(t, "release_admin", out.GetActor().GetRole())
}

// The projection used to carry four of the ten fields, which is what made the
// masked actor undisplayable in the first place.
func TestToProtoAuditEvent_ProjectsEveryStoredField(t *testing.T) {
	out := toProtoAuditEvent(sampleEvent(), true)

	assert.Equal(t, "ev-1", out.GetId())
	assert.Equal(t, "release_definition", out.GetResourceType())
	assert.Equal(t, "def-1", out.GetResourceId())
	assert.Equal(t, "create", out.GetAction())
	assert.Equal(t, "success", out.GetStatus())
	assert.EqualValues(t, 42, out.GetDurationMs())
	assert.Equal(t, "added a value", out.GetChangeSummary())
	assert.Equal(t, map[string]string{"k": "v"}, out.GetMetadata())
	require.NotNil(t, out.GetCreatedAt())
	assert.Equal(t, time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC), out.GetCreatedAt().AsTime())
}

func TestCanSeeAuditActorDetails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		roles []string
		want  bool
	}{
		{"platform admin", []string{"platform_admin"}, true},
		{"release admin", []string{"release_admin"}, true},
		{"viewer", []string{"viewer"}, false},
		{"no roles", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := authctx.WithActor(context.Background(), authctx.Actor{UserID: "u-1", OrganizationID: "org-1", Roles: tc.roles})
			assert.Equal(t, tc.want, canSeeAuditActorDetails(ctx))
		})
	}
	assert.False(t, canSeeAuditActorDetails(context.Background()), "an unauthenticated context sees no actor details")
}
