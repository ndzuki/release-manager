package auth

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// REQ-026's error model names invalid_role alongside duplicate_member and the
// other domain codes. It was the last one still reported as a bare status with a
// prose message, so a client could not branch on it.
func TestAddMember_InvalidRoleReasonCode(t *testing.T) {
	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	adminID, orgID := seedReleaseAdmin(t, st)
	_, err := svc.AddMember(withUser(context.Background(), adminID), connect.NewRequest(&authv1.AddMemberRequest{
		OrgId:  orgID,
		UserId: newID(),
		Role:   "not-a-role",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, reasonInvalidRole, reasonCodeOf(t, err))
}

func TestUpdateMemberRole_InvalidRoleReasonCode(t *testing.T) {
	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	ctx := context.Background()
	adminID, orgID := seedReleaseAdmin(t, st)
	targetID := newID()
	seedUser(t, st, targetID)
	seedOrgMember(t, st, orgID, targetID, store.RoleViewer)

	_, err := svc.UpdateMemberRole(withUser(ctx, adminID), connect.NewRequest(&authv1.UpdateMemberRoleRequest{
		OrgId:   orgID,
		UserId:  targetID,
		NewRole: "not-a-role",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, reasonInvalidRole, reasonCodeOf(t, err))
}
