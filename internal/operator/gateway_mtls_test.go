package operator_test

// Prototype Gate (plan v1 Steps 3+4, risk: high): validates the mixed mTLS
// contract — Enroll without a client certificate, CommandStream with a signed
// client certificate, reconnect re-establishing the unique online session,
// and rejection of certificate-less CommandStream on the gateway path.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

// newGatewayStore opens an in-memory store with one active customer/cluster
// and a pending enrollment token. Unlike newTestSvc it creates no operator,
// so the mTLS identity guard starts from a clean slate.
func newGatewayStore(t *testing.T, token string) store.Store {
	t.Helper()
	st, err := sqlitestore.Open("file::memory:?cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	cust := &store.Customer{ID: "cust-1", Name: "test-customer", Slug: "test", Status: store.CustomerActive, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, st.Customers().Create(ctx, cust))
	clus := &store.Cluster{ID: "clus-1", Name: "test-cluster", CustomerID: "cust-1", Status: store.ClusterActive, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, st.Clusters().Create(ctx, clus))
	require.NoError(t, st.EnrollmentTokens().Create(ctx, &store.EnrollmentToken{
		ID: "tok-1", CustomerID: "cust-1", ClusterID: "clus-1", Token: token,
		OperatorName: "clus-1", // must match the CSR CommonName (REQ-053 token binding)
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	return st
}

// newGatewayServer starts a TLS httptest server that mirrors the production
// gateway: VerifyClientCertIfGiven with the CA pool, a CA-signed server
// certificate, and the TLS state injected into the request context so
// CommandStream can enforce the mTLS identity path.
func newGatewayServer(t *testing.T, st store.Store, caInst *ca.CA) (baseURL string, pool *x509.CertPool) {
	t.Helper()
	svc, err := operator.NewService(st, slog.New(slog.DiscardHandler), operator.WithCA(caInst))
	require.NoError(t, err)
	mux := http.NewServeMux()
	path, handler := operatorv1connect.NewOperatorServiceHandler(svc)
	mux.Handle(path, handler)

	serverCertPEM, serverKeyPEM, err := caInst.SignServerCert([]string{"operator-gateway.dev.release-manager.local", "localhost"})
	require.NoError(t, err)
	serverCert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	require.NoError(t, err)

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			r = r.WithContext(operator.WithTLSState(r.Context(), r.TLS))
		}
		mux.ServeHTTP(w, r)
	}))
	// Serve() auto-configures the HTTP/2 handler only when the TLS config
	// advertises h2 via ALPN; httptest would otherwise default to http/1.1.
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    caInst.CertPool(),
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2", "http/1.1"},
	}
	ts.EnableHTTP2 = true
	ts.StartTLS()
	// The server certificate carries DNS SANs only (production hostnames);
	// point the client at localhost so the name matches.
	ts.URL = strings.Replace(ts.URL, "127.0.0.1", "localhost", 1)
	t.Cleanup(ts.Close)
	return ts.URL, caInst.CertPool()
}

// newAgentKeyAndCSR generates an Ed25519 agent key and a CSR carrying both the
// canonical SAN (<cluster>.<customer>.rm) and the flat identifiers the current
func newAgentKeyAndCSR(t *testing.T, customerID, clusterID string) (keyPEM, csrPEM []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: clusterID},
		DNSNames: []string{strings.ToLower(customerID), strings.ToLower(clusterID), strings.ToLower(clusterID + "." + customerID + ".rm")},
	}, priv)
	require.NoError(t, err)
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
}

func enrollViaGateway(t *testing.T, baseURL string, pool *x509.CertPool, token, customerID, clusterID, operatorID string, csrPEM []byte) *operatorv1.EnrollResponse {
	client := operatorv1connect.NewOperatorServiceClient(&http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}, ForceAttemptHTTP2: true},
	}, baseURL)
	resp, err := client.Enroll(context.Background(), connect.NewRequest(&operatorv1.EnrollRequest{
		EnrollmentToken: token,
		CustomerId:      customerID,
		ClusterId:       clusterID,
		OperatorId:      operatorID,
		CsrPem:          csrPEM,
	}))
	require.NoError(t, err)
	return resp.Msg
}

// openGatewayStream opens a CommandStream with the given client certificate
// and returns the SessionEstablished message. The stream is closed via
// t.Cleanup so the server handler and the httptest server can wind down.
func openGatewayStream(t *testing.T, baseURL string, pool *x509.CertPool, cert tls.Certificate, operatorID, instanceID string) (*operatorv1.SessionEstablished, error) {
	t.Helper()
	client := operatorv1connect.NewOperatorServiceClient(&http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:      pool,
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
		}, ForceAttemptHTTP2: true},
	}, baseURL)
	stream := client.CommandStream(context.Background())
	t.Cleanup(func() {
		stream.CloseRequest()  //nolint:errcheck // stream is already terminating
		stream.CloseResponse() //nolint:errcheck // stream is already terminating
	})
	if err := stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{Hello: &operatorv1.Hello{
			OperatorId: operatorID, InstanceId: instanceID, Version: "test-1.0",
		}},
	}); err != nil {
		return nil, err
	}
	resp, err := stream.Receive()
	if err != nil {
		return nil, err
	}
	est := resp.GetSessionEstablished()
	require.NotNil(t, est, "first response must be SessionEstablished")
	return est, nil
}
func TestGatewayMTLSEnrollAndStreamEstablish(t *testing.T) {
	token := "gateway-test-token"
	st := newGatewayStore(t, token)
	caInst, err := ca.New(ca.Config{})
	require.NoError(t, err)

	baseURL, pool := newGatewayServer(t, st, caInst)
	keyPEM, csrPEM := newAgentKeyAndCSR(t, "cust-1", "clus-1")
	opID := uuid.New().String()

	// PASS 1: Enroll without a client certificate succeeds (token + CSR).
	enrolled := enrollViaGateway(t, baseURL, pool, token, "cust-1", "clus-1", opID, csrPEM)
	require.NotEmpty(t, enrolled.GetSessionId())
	require.NotEmpty(t, enrolled.GetCertificatePem())
	enrolledOperator, err := st.Operators().GetByClusterID(context.Background(), "clus-1")
	require.NoError(t, err)
	require.Len(t, enrolledOperator.CertSerial, 20, "ADR-018 serial is 10 digest bytes encoded as hex")
	_, err = hex.DecodeString(enrolledOperator.CertSerial)
	require.NoError(t, err)
	agentCert, err := tls.X509KeyPair(enrolled.GetCertificatePem(), keyPEM)
	require.NoError(t, err)
	// PASS 2: CommandStream with the signed certificate → SessionEstablished.
	// Hello takes over session establishment (REQ-044 D-57), so the fresh
	// session_id supersedes the Enroll placeholder session.
	est, err := openGatewayStream(t, baseURL, pool, agentCert, opID, "inst-1")
	require.NoError(t, err)
	require.NotEmpty(t, est.GetSessionId())
	require.NotEqual(t, enrolled.GetSessionId(), est.GetSessionId())
	active, err := st.Sessions().GetActiveByOperator(context.Background(), opID)
	require.NoError(t, err)
	require.Equal(t, est.GetSessionId(), active.ID)

	// PASS 3: Reconnect with the same certificate and instance re-establishes
	// the unique online session; the previous session goes offline
	// (REQ-044: same instance_id may replace; a different instance_id while
	// the old session is online returns duplicate_session).
	est2, err := openGatewayStream(t, baseURL, pool, agentCert, opID, "inst-1")
	require.NoError(t, err)
	require.NotEqual(t, est.GetSessionId(), est2.GetSessionId())

	active, err = st.Sessions().GetActiveByOperator(context.Background(), opID)
	require.NoError(t, err)
	require.Equal(t, est2.GetSessionId(), active.ID)
	oldSess, err := st.Sessions().Get(context.Background(), est.GetSessionId())
	require.NoError(t, err)
	require.Equal(t, store.SessionOffline, oldSess.Status)

	// Token is consumed (single-use).
	used, err := st.EnrollmentTokens().GetByToken(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, store.TokenStateUsed, used.State)
}

func TestGatewayCommandStreamRequiresClientCert(t *testing.T) {
	st := newGatewayStore(t, "gateway-test-token-2")
	caInst, err := ca.New(ca.Config{})
	require.NoError(t, err)
	baseURL, pool := newGatewayServer(t, st, caInst)
	client := operatorv1connect.NewOperatorServiceClient(&http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}, ForceAttemptHTTP2: true},
	}, baseURL)
	stream := client.CommandStream(context.Background())
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{Hello: &operatorv1.Hello{OperatorId: "op-1"}},
	}))
	_, err = stream.Receive()
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestGatewayStreamRejectsSerialMismatch(t *testing.T) {
	token := "gateway-test-token-3"
	st := newGatewayStore(t, token)
	caInst, err := ca.New(ca.Config{})
	require.NoError(t, err)

	baseURL, pool := newGatewayServer(t, st, caInst)
	keyPEM, csrPEM := newAgentKeyAndCSR(t, "cust-1", "clus-1")
	opID := uuid.New().String()

	enrolled := enrollViaGateway(t, baseURL, pool, token, "cust-1", "clus-1", opID, csrPEM)
	agentCert, err := tls.X509KeyPair(enrolled.GetCertificatePem(), keyPEM)
	require.NoError(t, err)

	// Simulate a renew that rotated the registered serial while the agent still
	// holds the old certificate (ADR-018: old cert must be rejected).
	op, err := st.Operators().GetByClusterID(context.Background(), "clus-1")
	require.NoError(t, err)
	op.CertSerial = "deadbeef00"
	require.NoError(t, st.Operators().Update(context.Background(), op))

	_, err = openGatewayStream(t, baseURL, pool, agentCert, opID, "inst-1")
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}
