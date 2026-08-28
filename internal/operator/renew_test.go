package operator

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// TestRenewCertificateEnforcesWindowIdentityStatusAndSerial covers
// AC-015-09/10/11/12: the renew window, payload identity consistency, state
// guards, and the ADR-018 serial authority.
func TestRenewCertificateEnforcesWindowIdentityStatusAndSerial(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*store.Operator, *certificateIdentity)
		requestedID string
		wantReason  string
		wantCode    connect.Code
	}{
		{name: "too early", mutate: func(operator *store.Operator, _ *certificateIdentity) {
			expiresAt := time.Now().UTC().Add(50 * time.Minute)
			operator.CertificateExpiresAt = &expiresAt
		}, wantReason: reasonRenewTooEarly, wantCode: connect.CodeFailedPrecondition},
		{name: "payload identity mismatch", requestedID: "other-operator", wantReason: reasonCertificateInvalid, wantCode: connect.CodeUnauthenticated},
		{name: "superseded", mutate: func(operator *store.Operator, _ *certificateIdentity) {
			operator.Status = store.OperatorSuperseded
		}, wantReason: reasonOperatorSuperseded, wantCode: connect.CodePermissionDenied},
		{name: "revoked", mutate: func(operator *store.Operator, _ *certificateIdentity) {
			operator.Status = store.OperatorRevoked
		}, wantReason: reasonOperatorRevoked, wantCode: connect.CodePermissionDenied},
		{name: "old certificate", mutate: func(_ *store.Operator, identity *certificateIdentity) {
			identity.Serial = "00112233445566778899"
		}, wantReason: reasonCertReplaced, wantCode: connect.CodePermissionDenied},
		{name: "inside window"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, st, operatorRecord, certificate := newRenewFixture(t)
			identity := certificateIdentity{
				OperatorName: operatorRecord.Name,
				CustomerID:   operatorRecord.CustomerID,
				ClusterID:    operatorRecord.ClusterID,
				DNSName:      "cluster-1.customer-1.rm",
				Serial:       ca.CertSerial(certificate),
			}
			if test.mutate != nil {
				test.mutate(operatorRecord, &identity)
				require.NoError(t, st.Operators().Update(t.Context(), operatorRecord))
			}
			requestedID := operatorRecord.ID
			if test.requestedID != "" {
				requestedID = test.requestedID
			}
			response, err := svc.RenewCertificate(WithCertificateIdentity(t.Context(), identity), connect.NewRequest(&operatorv1.RenewCertificateRequest{
				OperatorId: requestedID,
				CsrPem:     lifecycleCSR(t, operatorRecord.Name, "cluster-1.customer-1.rm"),
			}))
			if test.wantReason != "" {
				require.Error(t, err)
				assert.Equal(t, test.wantCode, connect.CodeOf(err))
				assert.Equal(t, test.wantReason, operatorReason(err))
				return
			}
			require.NoError(t, err)
			require.NotNil(t, response)
			updated, err := st.Operators().Get(t.Context(), operatorRecord.ID)
			require.NoError(t, err)
			assert.NotEqual(t, identity.Serial, updated.CertSerial)
			assert.NotContains(t, string(response.Msg.GetCertificatePem()), "PRIVATE KEY")
			assert.Equal(t, int64(svc.ca.TTL().Seconds()), response.Msg.GetTtlSeconds())
		})
	}
}

// TestCertificateIdentityGuardRejectsSupersededRevokedAndReplaced covers
// AC-015-03/11 at the guard seam: superseded/revoked identities and replaced
// certificates are rejected with the stable reason codes.
func TestCertificateIdentityGuardRejectsSupersededRevokedAndReplaced(t *testing.T) {
	tests := []struct {
		name       string
		status     store.OperatorStatus
		peerSerial string
		wantReason string
	}{
		{name: "superseded", status: store.OperatorSuperseded, wantReason: reasonOperatorSuperseded},
		{name: "revoked", status: store.OperatorRevoked, wantReason: reasonOperatorRevoked},
		{name: "replaced", status: store.OperatorActive, peerSerial: "old-certificate", wantReason: reasonCertReplaced},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, st, operatorRecord, certificate := newRenewFixture(t)
			operatorRecord.Status = test.status
			require.NoError(t, st.Operators().Update(t.Context(), operatorRecord))
			identity := certificateIdentity{
				OperatorName: operatorRecord.Name,
				CustomerID:   operatorRecord.CustomerID,
				ClusterID:    operatorRecord.ClusterID,
				DNSName:      "cluster-1.customer-1.rm",
				Serial:       ca.CertSerial(certificate),
			}
			if test.peerSerial != "" {
				identity.Serial = test.peerSerial
			}
			err := validateCertificateIdentity(operatorRecord, identity)
			require.Error(t, err)
			assert.Equal(t, test.wantReason, operatorReason(err))
		})
	}
}

// TestOperatorLifecycleAuditsEnrollmentSupersedeRenew covers AC-015-17: the
// enrolled/superseded/renewed events carry actor/scope/request_id metadata and
// never contain token plaintext or private key material.
func TestOperatorLifecycleAuditsEnrollmentSupersedeRenew(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	ctx := t.Context()
	now := time.Now().UTC()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: "customer-audit", Name: "Audit Customer", Slug: "audit-customer", Status: store.CustomerActive, CreatedAt: now, UpdatedAt: now}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: "cluster-audit", Name: "Audit Cluster", CustomerID: "customer-audit", Status: store.ClusterActive, CreatedAt: now, UpdatedAt: now}))

	logger := slog.New(slog.DiscardHandler)
	emitter := audit.NewEmitter(st.AuditEvents(), logger, audit.EmitterConfig{BufferSize: 16, BatchSize: 16, FlushInterval: time.Hour, SpoolPath: t.TempDir() + "/audit.jsonl"})
	t.Cleanup(func() { require.NoError(t, emitter.Shutdown(context.Background())) })
	authority, err := ca.New(ca.Config{TTL: time.Hour})
	require.NoError(t, err)
	svc, err := NewService(st, logger, WithCA(authority), WithRenewBeforeRatio(1), WithAudit(emitter))
	require.NoError(t, err)

	firstToken := seedLifecycleToken(t, st, "token-first", "operator-first")
	first := enrollLifecycleOperator(t, svc, firstToken, "operator-first")
	secondToken := seedLifecycleToken(t, st, "token-second", "operator-second")
	second := enrollLifecycleOperator(t, svc, secondToken, "operator-second")

	secondOperator, err := st.Operators().Get(ctx, second.Msg.GetOperatorId())
	require.NoError(t, err)
	secondOperator.CertificateExpiresAt = new(now)
	require.NoError(t, st.Operators().Update(ctx, secondOperator))
	certificate := parseLifecycleCertificate(t, second.Msg.GetCertificatePem())
	renewContext := WithCertificateIdentity(ctx, certificateIdentity{
		OperatorName: secondOperator.Name,
		CustomerID:   secondOperator.CustomerID,
		ClusterID:    secondOperator.ClusterID,
		DNSName:      "cluster-audit.customer-audit.rm",
		Serial:       ca.CertSerial(certificate),
	})
	_, err = svc.RenewCertificate(renewContext, connect.NewRequest(&operatorv1.RenewCertificateRequest{
		OperatorId: secondOperator.ID,
		CsrPem:     lifecycleCSR(t, secondOperator.Name, "cluster-audit.customer-audit.rm"),
	}))
	require.NoError(t, err)
	require.NoError(t, emitter.Shutdown(context.Background()))

	firstEvents, err := st.AuditEvents().ListByResource(ctx, "operator", first.Msg.GetOperatorId())
	require.NoError(t, err)
	secondEvents, err := st.AuditEvents().ListByResource(ctx, "operator", second.Msg.GetOperatorId())
	require.NoError(t, err)

	assert.Contains(t, auditActions(firstEvents), "enrolled")
	assert.Contains(t, auditActions(firstEvents), "superseded")
	assert.Contains(t, auditActions(secondEvents), "enrolled")
	assert.Contains(t, auditActions(secondEvents), "renewed")
	for _, event := range append(firstEvents, secondEvents...) {
		assert.NotEmpty(t, event.Metadata["customer_id"])
		assert.NotEmpty(t, event.Metadata["cluster_id"])
		assert.NotEmpty(t, event.Metadata["request_id"])
		assert.NotContains(t, event.ChangeSummary, "PRIVATE KEY")
		assert.NotContains(t, event.ChangeSummary, firstToken)
		assert.NotContains(t, event.ChangeSummary, secondToken)
	}
}

// newRenewFixture enrolls a fresh operator and moves its certificate into the
// renew window (expires now), returning the service, store, record and the
// parsed issued certificate.
func newRenewFixture(t *testing.T) (*Service, *sqlitestore.Store, *store.Operator, *x509.Certificate) {
	t.Helper()
	svc, st := newEnrollmentService(t)
	rawToken := seedEnrollmentToken(t, st, time.Now().UTC().Add(time.Hour), store.TokenStatePending)
	response, err := svc.Enroll(t.Context(), connect.NewRequest(&operatorv1.EnrollRequest{
		EnrollmentToken: rawToken,
		CustomerId:      "customer-1",
		ClusterId:       "cluster-1",
		CsrPem:          lifecycleCSR(t, "operator-1", "cluster-1.customer-1.rm"),
	}))
	require.NoError(t, err)
	operatorRecord, err := st.Operators().Get(t.Context(), response.Msg.GetOperatorId())
	require.NoError(t, err)
	expiresAt := time.Now().UTC()
	operatorRecord.CertificateExpiresAt = &expiresAt
	require.NoError(t, st.Operators().Update(t.Context(), operatorRecord))
	return svc, st, operatorRecord, parseLifecycleCertificate(t, response.Msg.GetCertificatePem())
}

func seedLifecycleToken(t *testing.T, st *sqlitestore.Store, rawToken, operatorName string) string {
	t.Helper()
	require.NoError(t, st.EnrollmentTokens().Create(t.Context(), &store.EnrollmentToken{
		ID:                   rawToken,
		CustomerID:           "customer-audit",
		ClusterID:            "cluster-audit",
		OperatorName:         operatorName,
		TokenHash:            tokenHashHex(rawToken),
		State:                store.TokenStatePending,
		ExpiresAt:            time.Now().UTC().Add(time.Hour),
		CreatedByDisplayName: "release-admin",
		CreatedAt:            time.Now().UTC(),
	}))
	return rawToken
}

func enrollLifecycleOperator(t *testing.T, svc *Service, rawToken, operatorName string) *connect.Response[operatorv1.EnrollResponse] {
	t.Helper()
	request := connect.NewRequest(&operatorv1.EnrollRequest{
		EnrollmentToken: rawToken,
		CustomerId:      "customer-audit",
		ClusterId:       "cluster-audit",
		CsrPem:          lifecycleCSR(t, operatorName, "cluster-audit.customer-audit.rm"),
	})
	request.Header().Set("X-Request-ID", "request-"+operatorName)
	response, err := svc.Enroll(t.Context(), request)
	require.NoError(t, err)
	return response
}

func lifecycleCSR(t *testing.T, commonName, dnsName string) []byte {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: commonName},
		DNSNames: []string{dnsName},
	}, privateKey)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func parseLifecycleCertificate(t *testing.T, certificatePEM []byte) *x509.Certificate {
	t.Helper()
	block, rest := pem.Decode(certificatePEM)
	require.NotNil(t, block)
	require.Empty(t, rest)
	certificate, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return certificate
}

func auditActions(events []*store.AuditEvent) []string {
	actions := make([]string, 0, len(events))
	for _, event := range events {
		actions = append(actions, event.Action)
	}
	return actions
}

// tokenHashHex derives the hash-only token representation persisted by the
// store (REQ-015: the plaintext is never persisted).
func tokenHashHex(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}
