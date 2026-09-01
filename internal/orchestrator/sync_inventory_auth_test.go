package orchestrator

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// issueClientCert signs a client certificate from the test CA WITHOUT
// registering an Operator row, producing a verified-but-unregistered identity
// for the unknown-serial negative path (TASK-080, AC-080-04).
func issueClientCert(t *testing.T, caInst *ca.CA, commonName string) *x509.Certificate {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: commonName},
		DNSNames: []string{commonName},
	}, priv)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(csrDER)
	require.NoError(t, err)
	issued, err := caInst.SignCSR(csr, nil)
	require.NoError(t, err)
	certBlock, _ := pem.Decode(issued.PEM)
	require.NotNil(t, certBlock)
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	require.NoError(t, err)
	return cert
}

// newAuthTestIdentity issues an operator client certificate from the test CA
// and registers the matching Operator row (ADR-018 serial), mirroring the
// Enroll → GetByCertSerial trust path (TASK-080, AC-080-04).
func newAuthTestIdentity(t *testing.T, st store.Store, caInst *ca.CA, operatorID, customerID, clusterID string, status store.OperatorStatus) *x509.Certificate {
	t.Helper()
	cert := issueClientCert(t, caInst, clusterID)
	require.NoError(t, st.Operators().Create(context.Background(), &store.Operator{
		ID:         operatorID,
		Name:       clusterID,
		CustomerID: customerID,
		ClusterID:  clusterID,
		CertSerial: ca.CertSerial(cert),
		Status:     status,
	}))
	return cert
}

// newAuthTestRequest builds a SyncInventory request with the given identity
// fields, carrying the verified client certificate in the gateway TLS state.
func newAuthTestRequest(operatorID, customerID, clusterID string, cert *x509.Certificate) (context.Context, *connect.Request[orchestratorv1.SyncInventoryRequest]) {
	ctx := context.Background()
	if cert != nil {
		ctx = operator.WithTLSState(ctx, &tls.ConnectionState{
			VerifiedChains: [][]*x509.Certificate{{cert}},
		})
	}
	req := connect.NewRequest(&orchestratorv1.SyncInventoryRequest{
		OperatorId: operatorID,
		CustomerId: customerID,
		ClusterId:  clusterID,
		SyncId:     "sync-auth-test",
	})
	return ctx, req
}

// TestSyncInventoryCertAuthInterceptor covers AC-080-04 at the interceptor
// seam: trusted certificate serial lookup passes, and every negative path
// (no/invalid certificate → 401, field mismatch / revoked / superseded →
// PermissionDenied) is rejected before the handler (TASK-080, D-107=A).
func TestSyncInventoryCertAuthInterceptor(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	caInst, err := ca.New(ca.Config{TTL: 24 * time.Hour})
	require.NoError(t, err)

	const (
		operatorID = "operator-auth"
		customerID = "customer-auth"
		clusterID  = "cluster-auth"
	)
	cert := newAuthTestIdentity(t, st, caInst, operatorID, customerID, clusterID, store.OperatorActive)

	// A verified certificate whose serial has no matching Operator row must
	// be rejected as unauthenticated (proves the serial lookup is real).
	foreignCert := issueClientCert(t, caInst, "cluster-unregistered")
	revokedCert := newAuthTestIdentity(t, st, caInst, "operator-revoked", "customer-revoked", "cluster-revoked", store.OperatorRevoked)
	supersededCert := newAuthTestIdentity(t, st, caInst, "operator-superseded", "customer-superseded", "cluster-superseded", store.OperatorSuperseded)

	interceptor := NewSyncInventoryCertAuthInterceptor(st, slog.New(slog.DiscardHandler))

	tests := []struct {
		name       string
		req        func() (context.Context, *connect.Request[orchestratorv1.SyncInventoryRequest])
		wantErr    bool
		wantCode   connect.Code
		wantCalled bool
	}{
		{
			name: "trusted certificate with matching fields passes",
			req: func() (context.Context, *connect.Request[orchestratorv1.SyncInventoryRequest]) {
				return newAuthTestRequest(operatorID, customerID, clusterID, cert)
			},
			wantCalled: true,
		},
		{
			name: "no client certificate is unauthenticated",
			req: func() (context.Context, *connect.Request[orchestratorv1.SyncInventoryRequest]) {
				return newAuthTestRequest(operatorID, customerID, clusterID, nil)
			},
			wantErr:  true,
			wantCode: connect.CodeUnauthenticated,
		},
		{
			name: "unregistered certificate serial is unauthenticated",
			req: func() (context.Context, *connect.Request[orchestratorv1.SyncInventoryRequest]) {
				return newAuthTestRequest("operator-foreign", "customer-foreign", "cluster-foreign", foreignCert)
			},
			wantErr:  true,
			wantCode: connect.CodeUnauthenticated,
		},
		{
			name: "operator_id mismatch is permission denied",
			req: func() (context.Context, *connect.Request[orchestratorv1.SyncInventoryRequest]) {
				return newAuthTestRequest("operator-spoofed", customerID, clusterID, cert)
			},
			wantErr:  true,
			wantCode: connect.CodePermissionDenied,
		},
		{
			name: "customer_id mismatch is permission denied",
			req: func() (context.Context, *connect.Request[orchestratorv1.SyncInventoryRequest]) {
				return newAuthTestRequest(operatorID, "customer-spoofed", clusterID, cert)
			},
			wantErr:  true,
			wantCode: connect.CodePermissionDenied,
		},
		{
			name: "cluster_id mismatch is permission denied",
			req: func() (context.Context, *connect.Request[orchestratorv1.SyncInventoryRequest]) {
				return newAuthTestRequest(operatorID, customerID, "cluster-spoofed", cert)
			},
			wantErr:  true,
			wantCode: connect.CodePermissionDenied,
		},
		{
			name: "revoked operator is permission denied",
			req: func() (context.Context, *connect.Request[orchestratorv1.SyncInventoryRequest]) {
				return newAuthTestRequest("operator-revoked", "customer-revoked", "cluster-revoked", revokedCert)
			},
			wantErr:  true,
			wantCode: connect.CodePermissionDenied,
		},
		{
			name: "superseded operator is permission denied",
			req: func() (context.Context, *connect.Request[orchestratorv1.SyncInventoryRequest]) {
				return newAuthTestRequest("operator-superseded", "customer-superseded", "cluster-superseded", supersededCert)
			},
			wantErr:  true,
			wantCode: connect.CodePermissionDenied,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
				called = true
				return connect.NewResponse(&orchestratorv1.SyncInventoryResponse{}), nil
			})
			ctx, req := tt.req()
			resp, err := interceptor(next)(ctx, req)
			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, connect.CodeOf(err))
				assert.Nil(t, resp)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantCalled, called, "handler invocation must match expectation")
		})
	}
}
