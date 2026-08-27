package operator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// TestEnrollRejectsTokenFailuresWithStableReasons covers AC-015-01 and the
// REQ-015 error model: forged / expired / reused tokens and scope violations
// are rejected with the stable X-Reason-Code metadata.
func TestEnrollRejectsTokenFailuresWithStableReasons(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*testing.T, *sqlitestore.Store) string
		mutate     func(*testing.T, *sqlitestore.Store, *operatorv1.EnrollRequest)
		wantCode   connect.Code
		wantReason string
	}{
		{name: "forged", prepare: func(*testing.T, *sqlitestore.Store) string { return "forged" }, wantCode: connect.CodeUnauthenticated, wantReason: "invalid_token"},
		{name: "expired", prepare: func(t *testing.T, st *sqlitestore.Store) string {
			return seedEnrollmentToken(t, st, time.Now().UTC().Add(-time.Minute), store.TokenStatePending)
		}, wantCode: connect.CodeUnauthenticated, wantReason: "enroll_token_expired"},
		{name: "used", prepare: func(t *testing.T, st *sqlitestore.Store) string {
			return seedEnrollmentToken(t, st, time.Now().UTC().Add(time.Hour), store.TokenStateUsed)
		}, wantCode: connect.CodeUnauthenticated, wantReason: "token_reused"},
		{name: "scope mismatch", prepare: validEnrollmentToken, mutate: func(_ *testing.T, _ *sqlitestore.Store, request *operatorv1.EnrollRequest) {
			request.CustomerId = "other-customer"
		}, wantCode: connect.CodePermissionDenied, wantReason: "scope_mismatch"},
		{name: "customer disabled", prepare: validEnrollmentToken, mutate: func(t *testing.T, st *sqlitestore.Store, _ *operatorv1.EnrollRequest) {
			customer, err := st.Customers().Get(t.Context(), "customer-1")
			require.NoError(t, err)
			customer.Status = store.CustomerDisabled
			require.NoError(t, st.Customers().Update(t.Context(), customer, customer.Version))
		}, wantCode: connect.CodePermissionDenied, wantReason: "customer_disabled"},
		{name: "cluster disabled", prepare: validEnrollmentToken, mutate: func(t *testing.T, st *sqlitestore.Store, _ *operatorv1.EnrollRequest) {
			cluster, err := st.Clusters().Get(t.Context(), "cluster-1")
			require.NoError(t, err)
			cluster.Status = store.ClusterDisabled
			require.NoError(t, st.Clusters().Update(t.Context(), cluster, cluster.Version))
		}, wantCode: connect.CodePermissionDenied, wantReason: "cluster_disabled"},
		{name: "CSR SAN mismatch", prepare: validEnrollmentToken, mutate: func(t *testing.T, _ *sqlitestore.Store, request *operatorv1.EnrollRequest) {
			request.CsrPem = generateTestCSR(t, "operator-1", []string{"other-cluster.customer-1.rm"})
		}, wantCode: connect.CodePermissionDenied, wantReason: "csr_san_mismatch"},
		{name: "CSR invalid operator name", prepare: validEnrollmentToken, mutate: func(t *testing.T, _ *sqlitestore.Store, request *operatorv1.EnrollRequest) {
			// CN violates the DNS-label operator-name contract: csr_invalid,
			// not csr_san_mismatch (REQ-015 error model).
			request.CsrPem = generateTestCSR(t, "Operator_1", []string{"cluster-1.customer-1.rm"})
		}, wantCode: connect.CodeInvalidArgument, wantReason: "csr_invalid"},
		{name: "duplicate operator name", prepare: validEnrollmentToken, mutate: seedActiveOperatorOnCluster, wantCode: connect.CodeAlreadyExists, wantReason: "duplicate_operator_name"},
		{name: "operator name cross cluster", prepare: validEnrollmentToken, mutate: seedActiveOperatorOnOtherCluster, wantCode: connect.CodePermissionDenied, wantReason: "operator_name_cross_cluster"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, st := newEnrollmentService(t)
			rawToken := test.prepare(t, st)
			request := &operatorv1.EnrollRequest{
				EnrollmentToken: rawToken,
				CustomerId:      "customer-1",
				ClusterId:       "cluster-1",
				CsrPem:          generateTestCSR(t, "operator-1", []string{"cluster-1.customer-1.rm"}),
			}
			if test.mutate != nil {
				test.mutate(t, st, request)
			}
			_, err := svc.Enroll(t.Context(), connect.NewRequest(request))
			require.Error(t, err)
			assert.Equal(t, test.wantCode, connect.CodeOf(err))
			assert.Equal(t, test.wantReason, operatorReason(err))
		})
	}
}

// TestEnrollCreatesServerOwnedIdentityAndConsumesToken covers AC-015-04/05/16:
// the operator_id is center-generated, the certificate carries no private key,
// and the token is atomically consumed.
func TestEnrollCreatesServerOwnedIdentityAndConsumesToken(t *testing.T) {
	svc, st := newEnrollmentService(t)
	rawToken := seedEnrollmentToken(t, st, time.Now().UTC().Add(time.Hour), store.TokenStatePending)

	response, err := svc.Enroll(t.Context(), connect.NewRequest(&operatorv1.EnrollRequest{
		EnrollmentToken: rawToken,
		CustomerId:      "CUSTOMER-1",
		ClusterId:       "CLUSTER-1",
		CsrPem:          generateTestCSR(t, "operator-1", []string{"CLUSTER-1.CUSTOMER-1.RM"}),
		Capabilities:    map[string]string{"helm": "true"},
	}))
	require.NoError(t, err)
	require.NotEmpty(t, response.Msg.GetOperatorId())
	require.NotEmpty(t, response.Msg.GetSessionId())
	require.NotEmpty(t, response.Msg.GetCertificatePem())
	assert.NotContains(t, string(response.Msg.GetCertificatePem()), "PRIVATE KEY")
	expiresAt, err := time.Parse(time.RFC3339, response.Msg.GetCertificateExpiresAt())
	require.NoError(t, err)
	assert.True(t, expiresAt.After(time.Now().UTC()))

	token, err := st.EnrollmentTokens().GetByToken(t.Context(), rawToken)
	require.NoError(t, err)
	assert.Equal(t, store.TokenStateUsed, token.State)
	assert.Equal(t, response.Msg.GetOperatorId(), token.OperatorID)

	operator, err := st.Operators().Get(t.Context(), response.Msg.GetOperatorId())
	require.NoError(t, err)
	assert.Equal(t, "operator-1", operator.Name)
	assert.Equal(t, "customer-1", operator.CustomerID)
	assert.Equal(t, "cluster-1", operator.ClusterID)
	assert.NotEmpty(t, operator.CertSerial)
	assert.Len(t, operator.CertSerial, 20, "ADR-018 serial is 10 digest bytes encoded as hex")
	require.NotNil(t, operator.CertificateExpiresAt)

	session, err := st.Sessions().Get(t.Context(), response.Msg.GetSessionId())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"helm": "true"}, session.Capabilities)
}

// TestEnrollConsumesTokenExactlyOnceConcurrently covers AC-015-07: two
// concurrent Enrolls with the same token produce exactly one identity.
func TestEnrollConsumesTokenExactlyOnceConcurrently(t *testing.T) {
	svc, st := newEnrollmentService(t)
	rawToken := seedEnrollmentToken(t, st, time.Now().UTC().Add(time.Hour), store.TokenStatePending)
	request := func() *connect.Request[operatorv1.EnrollRequest] {
		return connect.NewRequest(&operatorv1.EnrollRequest{
			EnrollmentToken: rawToken,
			CustomerId:      "customer-1",
			ClusterId:       "cluster-1",
			CsrPem:          generateTestCSR(t, "operator-1", []string{"cluster-1.customer-1.rm"}),
		})
	}

	start := make(chan struct{})
	var wait sync.WaitGroup
	responses := make(chan *connect.Response[operatorv1.EnrollResponse], 2)
	errorsCh := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			response, err := svc.Enroll(t.Context(), request())
			responses <- response
			errorsCh <- err
		}()
	}
	close(start)
	wait.Wait()
	close(responses)
	close(errorsCh)

	successes := 0
	successfulOperatorID := ""
	reasons := make([]string, 0, 1)
	for response := range responses {
		if response != nil {
			successes++
			successfulOperatorID = response.Msg.GetOperatorId()
		}
	}
	for err := range errorsCh {
		if err != nil {
			reasons = append(reasons, operatorReason(err))
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, []string{"token_reused"}, reasons)
	token, err := st.EnrollmentTokens().GetByToken(t.Context(), rawToken)
	require.NoError(t, err)
	assert.Equal(t, successfulOperatorID, token.OperatorID)
	operators, err := st.Operators().ListByCluster(t.Context(), "cluster-1")
	require.NoError(t, err)
	assert.Len(t, operators, 1)
}

// TestEnrollSupersedesActiveOperatorAndClosesSession covers AC-015-08: a new
// enrollment on the same cluster supersedes the previous identity and closes
// its active session in the same transaction.
func TestEnrollSupersedesActiveOperatorAndClosesSession(t *testing.T) {
	svc, st := newEnrollmentService(t)
	now := time.Now().UTC()
	require.NoError(t, st.Operators().Create(t.Context(), &store.Operator{
		ID: "old-operator", Name: "old-operator", CustomerID: "customer-1", ClusterID: "cluster-1",
		CertSerial: "old-serial-0000000000", Status: store.OperatorActive, RegisteredAt: now,
	}))
	require.NoError(t, st.Sessions().Create(t.Context(), &store.Session{
		ID: "old-session", OperatorID: "old-operator", CustomerID: "customer-1", ClusterID: "cluster-1",
		Status: store.SessionOnline, StartedAt: now, LastHeartbeat: now, ExpiresAt: now.Add(time.Hour),
	}))

	rawToken := seedEnrollmentToken(t, st, time.Now().UTC().Add(time.Hour), store.TokenStatePending)
	response, err := svc.Enroll(t.Context(), connect.NewRequest(&operatorv1.EnrollRequest{
		EnrollmentToken: rawToken,
		CustomerId:      "customer-1",
		ClusterId:       "cluster-1",
		CsrPem:          generateTestCSR(t, "operator-1", []string{"cluster-1.customer-1.rm"}),
	}))
	require.NoError(t, err)

	old, err := st.Operators().Get(t.Context(), "old-operator")
	require.NoError(t, err)
	assert.Equal(t, store.OperatorSuperseded, old.Status)
	require.NotNil(t, old.SupersededAt)

	oldSession, err := st.Sessions().Get(t.Context(), "old-session")
	require.NoError(t, err)
	assert.Equal(t, store.SessionRevoked, oldSession.Status)

	// The new identity is the only active operator on the cluster.
	active, err := st.Operators().GetByClusterID(t.Context(), "cluster-1")
	require.NoError(t, err)
	assert.Equal(t, response.Msg.GetOperatorId(), active.ID)
}

func validEnrollmentToken(t *testing.T, st *sqlitestore.Store) string {
	t.Helper()
	return seedEnrollmentToken(t, st, time.Now().UTC().Add(time.Hour), store.TokenStatePending)
}

func seedActiveOperatorOnCluster(t *testing.T, st *sqlitestore.Store, _ *operatorv1.EnrollRequest) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, st.Operators().Create(t.Context(), &store.Operator{
		ID: uuid.NewString(), Name: "operator-1", CustomerID: "customer-1", ClusterID: "cluster-1",
		CertSerial: uuid.NewString(), Status: store.OperatorActive, RegisteredAt: now,
	}))
}

func seedActiveOperatorOnOtherCluster(t *testing.T, st *sqlitestore.Store, _ *operatorv1.EnrollRequest) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, st.Clusters().Create(t.Context(), &store.Cluster{
		ID: "cluster-2", Name: "Cluster 2", CustomerID: "customer-1", Status: store.ClusterActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, st.Operators().Create(t.Context(), &store.Operator{
		ID: uuid.NewString(), Name: "operator-1", CustomerID: "customer-1", ClusterID: "cluster-2",
		CertSerial: uuid.NewString(), Status: store.OperatorActive, RegisteredAt: now,
	}))
}

// newEnrollmentService opens a store seeded with customer-1/cluster-1 and
// returns a Service backed by a fresh CA (TTL 1h, renew ratio 0.5).
func newEnrollmentService(t *testing.T) (*Service, *sqlitestore.Store) {
	t.Helper()
	st := sqlitestore.OpenTest(t)
	now := time.Now().UTC()
	require.NoError(t, st.Customers().Create(t.Context(), &store.Customer{ID: "customer-1", Name: "Customer", Slug: "customer-1", Status: store.CustomerActive, CreatedAt: now, UpdatedAt: now}))
	require.NoError(t, st.Clusters().Create(t.Context(), &store.Cluster{ID: "cluster-1", Name: "Cluster", CustomerID: "customer-1", Status: store.ClusterActive, CreatedAt: now, UpdatedAt: now}))
	authority, err := ca.New(ca.Config{TTL: time.Hour})
	require.NoError(t, err)
	svc, err := NewService(st, slog.New(slog.DiscardHandler), WithCA(authority), WithRenewBeforeRatio(0.5))
	require.NoError(t, err)
	return svc, st
}

// seedEnrollmentToken persists a token row and returns the raw plaintext.
func seedEnrollmentToken(t *testing.T, st *sqlitestore.Store, expiresAt time.Time, state store.EnrollmentTokenState) string {
	t.Helper()
	rawToken := uuid.NewString() + uuid.NewString()
	hash := sha256.Sum256([]byte(rawToken))
	token := &store.EnrollmentToken{
		ID: "token-" + uuid.NewString(), CustomerID: "customer-1", ClusterID: "cluster-1",
		OperatorName: "operator-1", TokenHash: hex.EncodeToString(hash[:]), State: state,
		ExpiresAt: expiresAt, CreatedByDisplayName: "release-admin", CreatedAt: time.Now().UTC(),
	}
	if state == store.TokenStateUsed {
		usedAt := time.Now().UTC()
		token.UsedAt = &usedAt
		token.OperatorID = "used-operator"
	}
	require.NoError(t, st.EnrollmentTokens().Create(t.Context(), token))
	return rawToken
}

// operatorReason extracts the stable X-Reason-Code from a Connect error.
func operatorReason(err error) string {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr.Meta().Get("X-Reason-Code")
	}
	return ""
}
