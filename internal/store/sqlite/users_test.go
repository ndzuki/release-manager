package sqlite_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestUserListKeysetPagination(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	// Seed users in non-insertion order to prove ordering is by username.
	for i := 4; i >= 0; i-- {
		username := fmt.Sprintf("user-%02d", i)
		require.NoError(t, st.Users().Create(ctx, &store.User{
			ID: username + "-id", Username: username, PasswordHash: "hash",
		}))
	}

	page, err := st.Users().List(ctx, store.UserListQuery{PageSize: 2})
	require.NoError(t, err)
	assert.Len(t, page.Users, 2)
	assert.Equal(t, "user-00", page.Users[0].Username)
	assert.Equal(t, "user-01", page.Users[1].Username)
	assert.NotEmpty(t, page.NextCursor)

	next, err := st.Users().List(ctx, store.UserListQuery{PageSize: 2, Cursor: page.NextCursor})
	require.NoError(t, err)
	assert.Len(t, next.Users, 2)
	assert.Equal(t, "user-02", next.Users[0].Username)
	assert.Equal(t, "user-03", next.Users[1].Username)
	assert.NotEmpty(t, next.NextCursor)

	last, err := st.Users().List(ctx, store.UserListQuery{PageSize: 2, Cursor: next.NextCursor})
	require.NoError(t, err)
	assert.Len(t, last.Users, 1)
	assert.Equal(t, "user-04", last.Users[0].Username)
	assert.Empty(t, last.NextCursor)

	// Page-size clamping: 0 defaults to 20, oversized clamps to 100.
	all, err := st.Users().List(ctx, store.UserListQuery{})
	require.NoError(t, err)
	assert.Len(t, all.Users, 5)
	assert.Empty(t, all.NextCursor)

	// Malformed and foreign cursors are rejected structurally.
	_, err = st.Users().List(ctx, store.UserListQuery{Cursor: "!!!not-base64!!!"})
	assert.ErrorIs(t, err, store.ErrInvalidCursor)

	// "bm9uLXVzZXItY3Vyc29y" decodes to "non-user-cursor" — a foreign cursor
	// from another keyset stream must be rejected, not silently applied.
	_, err = st.Users().List(ctx, store.UserListQuery{Cursor: "bm9uLXVzZXItY3Vyc29y"})
	assert.ErrorIs(t, err, store.ErrInvalidCursor)
}

func TestCreateUserWithMembershipRollsBackOnMembershipFailure(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	user := &store.User{ID: "orphan-id", Username: "orphan", PasswordHash: "hash"}
	member := &store.OrganizationMember{OrgID: "missing-org", UserID: user.ID, Role: store.RoleViewer}
	err := st.Users().CreateWithMembership(ctx, user, member)
	require.Error(t, err)

	_, err = st.Users().GetByUsername(ctx, user.Username)
	assert.ErrorIs(t, err, store.ErrNotFound)
}
