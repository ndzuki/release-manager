package authorization

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// TestAuthorizeWriteRecoversOnceAFreshCredentialArrives covers the "a long run
// must not lose write ability" half of TASK-172 AC-2 without waiting for a real
// token TTL.
//
// The failure mode being pinned: the snapshot refresh forwards the last verified
// credential. When that credential expires the refresh fails — the call must then
// report the STABLE unavailable error (never the token text, which is what the
// browser used to render) and must NOT wedge the actor's cache entry, because the
// console refreshes its session and the next request carries a fresh cookie.
func TestAuthorizeWriteRecoversOnceAFreshCredentialArrives(t *testing.T) {
	orgID := "2b7f1c34-9d5e-4a6b-8c71-1f2e3d4c5b6a"
	customerID := "8e9f0a1b-2c3d-4e5f-9071-8293a4b5c6d7"
	handler := &snapshotHandler{
		response: &authv1.GetAuthorizationSnapshotResponse{
			OrganizationId: orgID, CustomerId: customerID, ActorId: "user-1", CanCreateValuesRevision: true,
			SourceVersion: 1, PolicyVersion: 1, Checkpoint: 1, Fresh: true,
		},
		err: connect.NewError(connect.CodeUnauthenticated, errors.New("token is expired")),
	}
	module, _, _ := newModuleFixture(t, handler)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}

	expired := authctx.WithVerifiedCredential(context.Background(), "expired-credential")
	err := module.AuthorizeWrite(expired, actor, customerID, store.AuthorizationCreateValues)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.NotContains(t, err.Error(), "expired")
	assert.Equal(t, "Bearer expired-credential", handler.lastAuthorization)

	// The session refreshes; the actor must converge again. Two calls on purpose:
	// the first successful pull re-establishes the checkpoint and still fails
	// closed (the module's documented initial catch-up), the next one allows —
	// exactly the same shape as TestModuleFailsClosedOnInitialCatchupThenAllows,
	// but starting from a failed refresh rather than a cold cache.
	handler.err = nil
	fresh := authctx.WithVerifiedCredential(context.Background(), "fresh-credential")
	require.Error(t, module.AuthorizeWrite(fresh, actor, customerID, store.AuthorizationCreateValues))
	require.NoError(t, module.AuthorizeWrite(fresh, actor, customerID, store.AuthorizationCreateValues))
	assert.Equal(t, "Bearer fresh-credential", handler.lastAuthorization)
}

// TestModuleBackgroundPullBacksOffWhileCredentialIsStale covers the other half of
// TASK-172 AC-2: the background refresher must wait between failed attempts
// (bounded exponential backoff) instead of hammering the auth service, and must
// resume normally once the pull succeeds again.
func TestModuleBackgroundPullBacksOffWhileCredentialIsStale(t *testing.T) {
	orgID := "6c5d4e3f-2a1b-4c9d-8e7f-6a5b4c3d2e1f"
	customerID := "1a2b3c4d-5e6f-4071-8293-a4b5c6d7e8f9"
	handler := &snapshotHandler{
		response: &authv1.GetAuthorizationSnapshotResponse{
			OrganizationId: orgID, CustomerId: customerID, ActorId: "user-1", CanCreateValuesRevision: true,
			SourceVersion: 1, PolicyVersion: 1, Checkpoint: 1, Fresh: true,
		},
		err: connect.NewError(connect.CodeUnauthenticated, errors.New("token is expired")),
	}
	module, _, _ := newModuleFixture(t, handler)
	actor := authctx.Actor{UserID: "user-1", OrganizationID: orgID}
	ctx := authctx.WithVerifiedCredential(context.Background(), "stale-credential")

	require.Error(t, module.AuthorizeWrite(ctx, actor, customerID, store.AuthorizationCreateValues))
	require.Equal(t, 1, handler.calls)

	key := cacheKey{actorID: "user-1", organizationID: orgID, customerID: customerID}
	module.mu.RLock()
	entry := module.entries[key]
	require.NotNil(t, entry)
	backoff := entry.backoff
	nextPull := entry.nextPull
	module.mu.RUnlock()
	assert.Equal(t, time.Second, backoff, "first failure backs off one interval")
	assert.True(t, nextPull.After(time.Now()), "next pull is scheduled in the future")

	// Before the deadline the refresher must not call the service at all.
	module.pullDue(context.Background(), time.Now())
	assert.Equal(t, 1, handler.calls)

	// Once the credential is refreshed, the due pull succeeds and clears backoff.
	handler.err = nil
	module.pullDue(context.Background(), nextPull.Add(time.Millisecond))
	assert.Equal(t, 2, handler.calls)
	module.mu.RLock()
	entry = module.entries[key]
	backoffAfter := entry.backoff
	initialized := entry.initialized
	module.mu.RUnlock()
	assert.Zero(t, backoffAfter, "a successful pull clears the backoff")
	assert.True(t, initialized)
}
