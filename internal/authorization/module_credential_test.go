package authorization

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// TestAuthorizeWriteForwardsVerifiedCredential pins ADR-028 clause 5 for a
// browser (cookie) session.
//
// A cookie-authenticated console sends NO Authorization header. Before the fix
// the module cached that empty header and forwarded nothing, so the snapshot
// refresh was permanently unauthenticated and every browser write failed closed
// with the refresh's own error text (B4). The verified credential handed over by
// the auth interceptor must be what reaches the internal RPC.
func TestAuthorizeWriteForwardsVerifiedCredential(t *testing.T) {
	orgID := "3f1c2a48-2f6b-4d5e-9a71-3f1f2b7c9d10"
	customerID := "b7d1a0c4-51a9-4f0e-8f22-6c8f1f0a77bb"
	handler := &snapshotHandler{response: &authv1.GetAuthorizationSnapshotResponse{
		OrganizationId: orgID, CustomerId: customerID, ActorId: "user-1", CanCreateValuesRevision: true,
		SourceVersion: 1, PolicyVersion: 1, Checkpoint: 1, Fresh: true,
	}}
	module, _, _ := newModuleFixture(t, handler)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}

	// Cookie session: verified credential present, Authorization header absent.
	ctx := authctx.WithVerifiedCredential(context.Background(), "cookie-access-token")
	// The first call for a new actor+scope always fails closed while the
	// snapshot is being initialized (same contract as
	// TestModuleFailsClosedOnInitialCatchupThenAllows); the pull still happens,
	// which is what this test observes.
	require.Error(t, module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationCreateValues))
	assert.Equal(t, "Bearer cookie-access-token", handler.lastAuthorization)
	require.NoError(t, module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationCreateValues))
}

// TestAuthorizeWriteFallsBackToAuthorizationHeader covers the bearer clients
// that existed before ADR-028 and must keep working unchanged.
func TestAuthorizeWriteFallsBackToAuthorizationHeader(t *testing.T) {
	orgID := "9c2b7e10-4a3d-4c7b-8e55-1d0f2a3b4c5d"
	customerID := "0a1b2c3d-4e5f-4061-8273-8495a6b7c8d9"
	handler := &snapshotHandler{response: &authv1.GetAuthorizationSnapshotResponse{
		OrganizationId: orgID, CustomerId: customerID, ActorId: "user-1", CanCreateValuesRevision: true,
		SourceVersion: 1, PolicyVersion: 1, Checkpoint: 1, Fresh: true,
	}}
	module, _, _ := newModuleFixture(t, handler)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}

	ctx := authctx.WithAuthorizationHeader(context.Background(), "Bearer cli-access-token")
	require.Error(t, module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationCreateValues))
	assert.Equal(t, "Bearer cli-access-token", handler.lastAuthorization)
	require.NoError(t, module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationCreateValues))
}

// TestAuthorizeWriteHidesSnapshotPullFailure pins the second half of the B4 fix:
// the caller must receive a stable, non-leaking error.
//
// The browser used to read the snapshot REFRESH failure verbatim —
// "invalid token: parse access token: token has invalid claims: token is
// expired" — while its own session was healthy. The cause belongs in the WARN
// log and the decision record, not in the user-facing error.
func TestAuthorizeWriteHidesSnapshotPullFailure(t *testing.T) {
	orgID := "5e4d3c2b-1a09-4877-9665-4433221100ff"
	customerID := "ff001122-3344-4556-8778-99aabbccddee"
	handler := &snapshotHandler{
		err: connect.NewError(connect.CodeUnauthenticated,
			errors.New("invalid token: parse access token: token has invalid claims: token is expired")),
	}
	module, _, _ := newModuleFixture(t, handler)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}

	err := module.AuthorizeWrite(context.Background(), actor, customerID, store.AuthorizationCreateValues)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Equal(t, "AUTHORIZATION_SNAPSHOT_STALE", reasonCode(err))
	assert.NotContains(t, err.Error(), "token is expired")
	assert.NotContains(t, err.Error(), "invalid token")
}
