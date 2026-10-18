package operator_test

// TASK-361 regression: the session row CommandStream establishes on the mTLS
// gateway path must carry the operator's customer_id / cluster_id.
//
// Before the fix the session literal omitted both fields, so the row kept the
// column default '' while the Enroll placeholder row was correct (Enroll copies
// the operator's scope). A cluster-scoped read of sessions therefore had to join
// operators, and a direct sessions.cluster_id lookup silently missed the live
// session (observed on the rm-prod dev cluster: 6 of 9 rows empty).

import (
	"context"
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/operator/ca"
)

func TestCommandStreamSessionCarriesOperatorIdentity(t *testing.T) {
	token := "session-identity-token"
	st := newGatewayStore(t, token)
	caInst, err := ca.New(ca.Config{})
	require.NoError(t, err)

	baseURL, pool := newGatewayServer(t, st, caInst)
	// The token carries the scope the gateway binds the CSR to; reading it from
	// the store (instead of repeating the seed literals) keeps the fixture and the
	// assertion on one source.
	pending, err := st.EnrollmentTokens().GetByToken(context.Background(), token)
	require.NoError(t, err)
	keyPEM, csrPEM := newAgentKeyAndCSR(t, pending.CustomerID, pending.ClusterID)

	enrolled := enrollViaGateway(t, baseURL, pool, token, pending.CustomerID, pending.ClusterID, csrPEM)
	op, err := st.Operators().GetByClusterID(context.Background(), pending.ClusterID)
	require.NoError(t, err)
	agentCert, err := tls.X509KeyPair(enrolled.GetCertificatePem(), keyPEM)
	require.NoError(t, err)

	// Hello takes over session establishment (REQ-044 D-57): this is the row the
	// CommandStream path writes, not the Enroll placeholder.
	est, err := openGatewayStream(t, baseURL, pool, agentCert, enrolled.GetOperatorId())
	require.NoError(t, err)
	require.NotEmpty(t, est.GetSessionId())
	require.NotEqual(t, enrolled.GetSessionId(), est.GetSessionId())

	active, err := st.Sessions().GetActiveByOperator(context.Background(), enrolled.GetOperatorId())
	require.NoError(t, err)
	require.Equal(t, est.GetSessionId(), active.ID)
	require.Equal(t, op.CustomerID, active.CustomerID, "CommandStream session must carry the operator's customer")
	require.Equal(t, op.ClusterID, active.ClusterID, "CommandStream session must carry the operator's cluster")

	// The Enroll placeholder row must keep carrying them too, so the fix does not
	// trade one empty row for another.
	placeholder, err := st.Sessions().Get(context.Background(), enrolled.GetSessionId())
	require.NoError(t, err)
	require.Equal(t, op.CustomerID, placeholder.CustomerID)
	require.Equal(t, op.ClusterID, placeholder.ClusterID)
}
