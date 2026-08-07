package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestAuthorizationStoreApplyAndCheckpoint(t *testing.T) {
	st := sqlite.OpenTest(t)
	ctx := context.Background()

	initial, err := st.Authorization().Load(ctx)
	require.NoError(t, err)
	assert.Zero(t, initial.SourceVersion)
	assert.Zero(t, initial.PolicyVersion)

	applied, err := st.Authorization().Apply(ctx, store.AuthorizationApplyCommand{
		ExpectedSourceVersion: 0,
		ExpectedPolicyVersion: 0,
		Mutation:              store.AuthorizationGrantChanged,
		Grants: []store.CapabilityGrant{{
			OrganizationID: "org-1", Subject: "user-1", Action: store.AuthorizationExecuteEmergency,
			GrantedBy: "admin-1",
		}},
		Rules: []store.CasbinRule{{
			PType: "p", V0: "user-1", V1: "org-1", V2: "release", V3: string(store.AuthorizationExecuteEmergency),
		}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, applied.SourceVersion)
	assert.EqualValues(t, 1, applied.PolicyVersion)
	require.Len(t, applied.Grants, 1)
	assert.False(t, applied.Grants[0].Revoked)
	require.Len(t, applied.Rules, 1)

	err = st.Authorization().SaveCheckpoint(ctx, store.AuthorizationCheckpoint{
		OrganizationID: "org-1", CustomerID: "customer-1", SourceVersion: 1, PolicyVersion: 1, Fresh: true,
	})
	require.NoError(t, err)
	checkpoint, err := st.Authorization().GetCheckpoint(ctx, "org-1", "customer-1")
	require.NoError(t, err)
	assert.True(t, checkpoint.Fresh)
	assert.EqualValues(t, 1, checkpoint.SourceVersion)
}

func TestAuthorizationStoreRejectsStaleApply(t *testing.T) {
	st := sqlite.OpenTest(t)
	ctx := context.Background()
	_, err := st.Authorization().Apply(ctx, store.AuthorizationApplyCommand{
		ExpectedSourceVersion: 0,
		ExpectedPolicyVersion: 0,
		Mutation:              store.AuthorizationMembershipChanged,
	})
	require.NoError(t, err)

	_, err = st.Authorization().Apply(ctx, store.AuthorizationApplyCommand{
		ExpectedSourceVersion: 0,
		ExpectedPolicyVersion: 0,
		Mutation:              store.AuthorizationBindingChanged,
	})
	assert.ErrorIs(t, err, store.ErrOptimisticLock)

	snapshot, err := st.Authorization().Load(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, snapshot.SourceVersion)
	assert.Zero(t, snapshot.PolicyVersion)
}
