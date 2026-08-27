//go:build integration

package postgres_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TestOperatorLifecycle_UpdateCertificateCAS mirrors the SQLite contract
// (sqlite/operator_lifecycle_test.go) on the production engine: only the
// expected serial can rotate (ADR-018 CAS), a stale expectation fails closed,
// and a revoked operator cannot rotate.
func TestOperatorLifecycle_UpdateCertificateCAS(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	customerID, clusterID := seedOperatorManagementScope(t, st)

	op := &store.Operator{
		ID: "op-rotate-pg", Name: "op-rotate-pg", CustomerID: customerID, ClusterID: clusterID,
		CertSerial: "serial-pg-1", Status: store.OperatorActive,
	}
	require.NoError(t, st.Operators().Create(ctx, op))

	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Microsecond)
	require.NoError(t, st.OperatorLifecycle().UpdateCertificate(ctx, op.ID, "serial-pg-1", "serial-pg-2", expiresAt))
	got, err := st.Operators().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "serial-pg-2", got.CertSerial)
	require.NotNil(t, got.CertificateExpiresAt)
	assert.Equal(t, expiresAt.Format(time.RFC3339Nano), got.CertificateExpiresAt.UTC().Format(time.RFC3339Nano))

	// Stale expectation (concurrent renew lost the race) → conflict.
	assert.ErrorIs(t,
		st.OperatorLifecycle().UpdateCertificate(ctx, op.ID, "serial-pg-1", "serial-pg-3", expiresAt),
		store.ErrCertificateConflict)

	// Revoked operator cannot rotate.
	_, err = st.OperatorManagement().RevokeOperator(ctx, customerID, clusterID, op.ID, "test revoke",
		operatorAuditEvent("audit-rotate-pg", op.ID, "operator.revoked"))
	require.NoError(t, err)
	assert.ErrorIs(t,
		st.OperatorLifecycle().UpdateCertificate(ctx, op.ID, "serial-pg-2", "serial-pg-4", expiresAt),
		store.ErrCertificateConflict)
}

// TestOperatorLifecycle_UpdateCertificateConcurrentCAS proves the ADR-018
// authority on PostgreSQL: with N concurrent rotations carrying the same
// expected serial, exactly one wins and every loser gets ErrCertificateConflict
// (no duplicate-key surprises, last committed serial is authoritative).
func TestOperatorLifecycle_UpdateCertificateConcurrentCAS(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	customerID, clusterID := seedOperatorManagementScope(t, st)

	op := &store.Operator{
		ID: "op-rotate-pg-concurrent", Name: "op-rotate-pg-concurrent",
		CustomerID: customerID, ClusterID: clusterID,
		CertSerial: "serial-concurrent-1", Status: store.OperatorActive,
	}
	require.NoError(t, st.Operators().Create(ctx, op))

	const workers = 8
	start := make(chan struct{})
	winners := make(chan string, workers)
	conflicts := make(chan error, workers)
	unexpected := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := st.OperatorLifecycle().UpdateCertificate(ctx, op.ID, "serial-concurrent-1",
				fmt.Sprintf("serial-concurrent-%d", i+2), time.Now().UTC().Add(time.Hour))
			switch {
			case err == nil:
				winners <- fmt.Sprintf("serial-concurrent-%d", i+2)
			case errors.Is(err, store.ErrCertificateConflict):
				conflicts <- err
			default:
				unexpected <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(winners)
	close(conflicts)
	close(unexpected)

	var win []string
	for s := range winners {
		win = append(win, s)
	}
	var lost int
	for range conflicts {
		lost++
	}
	var others []error
	for err := range unexpected {
		others = append(others, err)
	}

	assert.Empty(t, others, "non-conflict errors: %v", others)
	assert.Len(t, win, 1, "exactly one concurrent rotation must win")
	assert.Equal(t, workers-1, lost, "every loser must get ErrCertificateConflict")

	got, err := st.Operators().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, win[0], got.CertSerial)
	assert.NotEqual(t, "serial-concurrent-1", got.CertSerial)
}

// TestOperatorLifecycle_DisableCustomerCascade mirrors the SQLite contract on
// PostgreSQL: every pending token, non-revoked operator and live session of
// the customer is revoked in one transaction, idempotently (first-write
// reason/time preserved), without touching another customer's scope.
func TestOperatorLifecycle_DisableCustomerCascade(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	customerID, clusterID := seedOperatorManagementScope(t, st)

	token := &store.EnrollmentToken{
		ID: uuid.NewString(), CustomerID: customerID, ClusterID: clusterID,
		OperatorName: "cascade-op", TokenHash: sha256HexTest("cascade-plaintext"),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	require.NoError(t, st.EnrollmentTokens().Create(ctx, token))
	op := &store.Operator{
		ID: "op-cascade-pg", Name: "cascade-op", CustomerID: customerID, ClusterID: clusterID,
		CertSerial: "cascade-serial-1", Status: store.OperatorActive,
	}
	require.NoError(t, st.Operators().Create(ctx, op))
	require.NoError(t, st.Sessions().Create(ctx, &store.Session{
		ID: "sess-cascade-pg", OperatorID: op.ID, CustomerID: customerID, ClusterID: clusterID,
		Status: store.SessionOnline,
	}))

	// Cross-scope non-interference guard: another customer stays untouched.
	otherCustomerID, otherClusterID := seedOperatorManagementScope(t, st)
	otherOp := &store.Operator{
		ID: "op-other-customer", Name: "other-op", CustomerID: otherCustomerID, ClusterID: otherClusterID,
		CertSerial: "other-serial-1", Status: store.OperatorActive,
	}
	require.NoError(t, st.Operators().Create(ctx, otherOp))

	result, err := st.OperatorLifecycle().DisableCustomer(ctx, customerID, "customer disabled")
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Contains(t, result.TokenIDs, token.ID)
	assert.Contains(t, result.OperatorIDs, op.ID)
	assert.Contains(t, result.SessionIDs, "sess-cascade-pg")

	gotToken, err := st.EnrollmentTokens().GetByToken(ctx, "cascade-plaintext")
	require.NoError(t, err)
	assert.Equal(t, store.TokenStateRevoked, gotToken.State)
	require.NotNil(t, gotToken.RevokedAt)

	gotOp, err := st.Operators().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.OperatorRevoked, gotOp.Status)
	assert.Equal(t, "customer disabled", gotOp.RevokeReason)
	require.NotNil(t, gotOp.RevokedAt)
	firstOperatorRevokedAt := gotOp.RevokedAt.UTC()

	gotSess, err := st.Sessions().Get(ctx, "sess-cascade-pg")
	require.NoError(t, err)
	assert.Equal(t, store.SessionRevoked, gotSess.Status)
	require.NotNil(t, gotSess.StatusReason)
	assert.Equal(t, store.SessionReasonCertRevoked, *gotSess.StatusReason)
	require.NotNil(t, gotSess.ClosedAt)

	// Idempotent re-disable: first-write reason/time preserved.
	time.Sleep(2 * time.Millisecond)
	again, err := st.OperatorLifecycle().DisableCustomer(ctx, customerID, "customer disabled again")
	require.NoError(t, err)
	assert.False(t, again.Changed)
	gotOp, err = st.Operators().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "customer disabled", gotOp.RevokeReason)
	assert.Equal(t, firstOperatorRevokedAt, gotOp.RevokedAt.UTC())

	// The other customer's operator stays active.
	otherGot, err := st.Operators().Get(ctx, otherOp.ID)
	require.NoError(t, err)
	assert.Equal(t, store.OperatorActive, otherGot.Status)
}

// TestOperatorLifecycle_DisableClusterCascade mirrors the SQLite cluster-scope
// contract on PostgreSQL with the same atomic semantics; the suspect session
// covers the IN (online, suspect) close branch.
func TestOperatorLifecycle_DisableClusterCascade(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	customerID, clusterID := seedOperatorManagementScope(t, st)

	token := &store.EnrollmentToken{
		ID: uuid.NewString(), CustomerID: customerID, ClusterID: clusterID,
		OperatorName: "cluster-op", TokenHash: sha256HexTest("cluster-plaintext"),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	require.NoError(t, st.EnrollmentTokens().Create(ctx, token))
	op := &store.Operator{
		ID: "op-cluster-cascade-pg", Name: "cluster-op", CustomerID: customerID, ClusterID: clusterID,
		CertSerial: "cluster-serial-1", Status: store.OperatorActive,
	}
	require.NoError(t, st.Operators().Create(ctx, op))
	require.NoError(t, st.Sessions().Create(ctx, &store.Session{
		ID: "sess-cluster-cascade-pg", OperatorID: op.ID, CustomerID: customerID, ClusterID: clusterID,
		Status: store.SessionSuspect,
	}))

	// Sibling cluster in the same customer stays untouched.
	otherClusterID := uuid.NewString()
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: otherClusterID, Name: "Sibling cluster", CustomerID: customerID}))
	otherOp := &store.Operator{
		ID: "op-sibling-cluster", Name: "sibling-op", CustomerID: customerID, ClusterID: otherClusterID,
		CertSerial: "sibling-serial-1", Status: store.OperatorActive,
	}
	require.NoError(t, st.Operators().Create(ctx, otherOp))

	result, err := st.OperatorLifecycle().DisableCluster(ctx, clusterID, "cluster disabled")
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Contains(t, result.TokenIDs, token.ID)
	assert.Contains(t, result.OperatorIDs, op.ID)
	assert.Contains(t, result.SessionIDs, "sess-cluster-cascade-pg")

	gotOp, err := st.Operators().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.OperatorRevoked, gotOp.Status)
	require.NotNil(t, gotOp.RevokedAt)
	firstOperatorRevokedAt := gotOp.RevokedAt.UTC()

	gotToken, err := st.EnrollmentTokens().GetByToken(ctx, "cluster-plaintext")
	require.NoError(t, err)
	assert.Equal(t, store.TokenStateRevoked, gotToken.State)

	gotSess, err := st.Sessions().Get(ctx, "sess-cluster-cascade-pg")
	require.NoError(t, err)
	assert.Equal(t, store.SessionRevoked, gotSess.Status)
	require.NotNil(t, gotSess.StatusReason)
	assert.Equal(t, store.SessionReasonCertRevoked, *gotSess.StatusReason)
	require.NotNil(t, gotSess.ClosedAt)

	// Idempotent re-disable: first-write time preserved.
	time.Sleep(2 * time.Millisecond)
	again, err := st.OperatorLifecycle().DisableCluster(ctx, clusterID, "cluster disabled again")
	require.NoError(t, err)
	assert.False(t, again.Changed)
	gotOp, err = st.Operators().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, firstOperatorRevokedAt, gotOp.RevokedAt.UTC())

	// The sibling cluster's operator stays active.
	otherGot, err := st.Operators().Get(ctx, otherOp.ID)
	require.NoError(t, err)
	assert.Equal(t, store.OperatorActive, otherGot.Status)
}
