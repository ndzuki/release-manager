package sqlite_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TestUpdateCertificateCAS verifies the ADR-018 serial rotation seam: only
// the expected serial can rotate; a stale expectation fails closed; revoked
// operators cannot rotate.
func TestUpdateCertificateCAS(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	customerID, clusterID := seedOperatorManagementScope(t, st)

	op := &store.Operator{
		ID: "op-rotate", Name: "op-rotate", CustomerID: customerID, ClusterID: clusterID,
		CertSerial: "serial-1", Status: store.OperatorActive,
	}
	require.NoError(t, st.Operators().Create(ctx, op))

	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	require.NoError(t, st.OperatorLifecycle().UpdateCertificate(ctx, op.ID, "serial-1", "serial-2", expiresAt))
	got, err := st.Operators().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "serial-2", got.CertSerial)
	require.NotNil(t, got.CertificateExpiresAt)
	assert.Equal(t, expiresAt.UTC().Format(time.RFC3339Nano), got.CertificateExpiresAt.UTC().Format(time.RFC3339Nano))

	// Stale expectation (concurrent renew lost the race) → conflict.
	assert.ErrorIs(t,
		st.OperatorLifecycle().UpdateCertificate(ctx, op.ID, "serial-1", "serial-3", expiresAt),
		store.ErrCertificateConflict)

	// Revoked operator cannot rotate.
	_, err = st.OperatorManagement().RevokeOperator(ctx, customerID, clusterID, op.ID, "test revoke", nil)
	require.NoError(t, err)
	assert.ErrorIs(t,
		st.OperatorLifecycle().UpdateCertificate(ctx, op.ID, "serial-2", "serial-4", expiresAt),
		store.ErrCertificateConflict)
}

// TestDisableCustomerCascade verifies the REQ-015 事务边界 4 cascade: every
// pending token, non-revoked operator and live session of the customer is
// revoked in one transaction, idempotently (first reason/time preserved).
func TestDisableCustomerCascade(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	customerID, clusterID := seedOperatorManagementScope(t, st)

	token := &store.EnrollmentToken{
		ID: uuid.NewString(), CustomerID: customerID, ClusterID: clusterID,
		OperatorName: "cascade-op", TokenHash: sha256Hex("cascade-plaintext"), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	require.NoError(t, st.EnrollmentTokens().Create(ctx, token))
	op := &store.Operator{
		ID: "op-cascade", Name: "cascade-op", CustomerID: customerID, ClusterID: clusterID,
		CertSerial: "cascade-serial-1", Status: store.OperatorActive,
	}
	require.NoError(t, st.Operators().Create(ctx, op))
	require.NoError(t, st.Sessions().Create(ctx, &store.Session{
		ID: "sess-cascade", OperatorID: op.ID, CustomerID: customerID, ClusterID: clusterID,
		Status: store.SessionOnline,
	}))

	result, err := st.OperatorLifecycle().DisableCustomer(ctx, customerID, "customer disabled")
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Contains(t, result.TokenIDs, token.ID)
	assert.Contains(t, result.OperatorIDs, op.ID)
	assert.Contains(t, result.SessionIDs, "sess-cascade")

	gotToken, err := st.EnrollmentTokens().GetByToken(ctx, "cascade-plaintext")
	require.NoError(t, err)
	assert.Equal(t, store.TokenStateRevoked, gotToken.State)
	require.NotNil(t, gotToken.RevokedAt)

	gotOp, err := st.Operators().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.OperatorRevoked, gotOp.Status)
	assert.Equal(t, "customer disabled", gotOp.RevokeReason)

	gotSess, err := st.Sessions().Get(ctx, "sess-cascade")
	require.NoError(t, err)
	assert.Equal(t, store.SessionRevoked, gotSess.Status)

	// Idempotent re-disable: first-write reason/time preserved.
	time.Sleep(time.Millisecond * 2)
	again, err := st.OperatorLifecycle().DisableCustomer(ctx, customerID, "customer disabled again")
	require.NoError(t, err)
	assert.False(t, again.Changed)
	gotOp, err = st.Operators().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "customer disabled", gotOp.RevokeReason)
	assert.Equal(t, result.Changed, true)
}

// TestDisableClusterCascade verifies the cluster-scope cascade with the same
// atomic semantics (irreversible; re-enable requires new enrollment).
func TestDisableClusterCascade(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	customerID, clusterID := seedOperatorManagementScope(t, st)

	token := &store.EnrollmentToken{
		ID: uuid.NewString(), CustomerID: customerID, ClusterID: clusterID,
		OperatorName: "cluster-op", TokenHash: sha256Hex("cluster-plaintext"), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	require.NoError(t, st.EnrollmentTokens().Create(ctx, token))
	op := &store.Operator{
		ID: "op-cluster-cascade", Name: "cluster-op", CustomerID: customerID, ClusterID: clusterID,
		CertSerial: "cluster-serial-1", Status: store.OperatorActive,
	}
	require.NoError(t, st.Operators().Create(ctx, op))
	require.NoError(t, st.Sessions().Create(ctx, &store.Session{
		ID: "sess-cluster-cascade", OperatorID: op.ID, CustomerID: customerID, ClusterID: clusterID,
		Status: store.SessionSuspect,
	}))

	result, err := st.OperatorLifecycle().DisableCluster(ctx, clusterID, "cluster disabled")
	require.NoError(t, err)
	assert.True(t, result.Changed)

	gotOp, err := st.Operators().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.OperatorRevoked, gotOp.Status)
	gotToken, err := st.EnrollmentTokens().GetByToken(ctx, "cluster-plaintext")
	require.NoError(t, err)
	assert.Equal(t, store.TokenStateRevoked, gotToken.State)
	gotSess, err := st.Sessions().Get(ctx, "sess-cluster-cascade")
	require.NoError(t, err)
	assert.Equal(t, store.SessionRevoked, gotSess.Status)
	require.NotNil(t, gotSess.StatusReason)
	assert.Equal(t, store.SessionReasonCertRevoked, *gotSess.StatusReason)
}
