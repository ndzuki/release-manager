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

// AC-026-06: adding an (org, user) pair that is already a member is a domain
// conflict the caller can act on. The store translates the primary-key violation
// into ErrDuplicateKey and the service reports duplicate_member; before this the
// handler collapsed it into CodeInternal, so a client could not tell "already a
// member" from "the server broke".
func TestAddMember_DuplicateIsDomainConflict(t *testing.T) {
	svc, st, cleanup := setupOrgService(t)
	defer cleanup()

	releaseAdminID, orgID := seedReleaseAdmin(t, st)
	ctx := withUser(context.Background(), releaseAdminID)
	targetUserID := newID()
	seedUser(t, st, targetUserID)

	request := func() *connect.Request[authv1.AddMemberRequest] {
		return connect.NewRequest(&authv1.AddMemberRequest{
			OrgId:  orgID,
			UserId: targetUserID,
			Role:   string(store.RoleDeployer),
		})
	}

	_, err := svc.AddMember(ctx, request())
	require.NoError(t, err)

	_, err = svc.AddMember(ctx, request())
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "duplicate_member", connectErr.Meta().Get("X-Reason-Code"),
		"clients branch on the reason code, not on the message text")
}
