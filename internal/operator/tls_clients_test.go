package operator_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/operator/ca"
)

// writeCAFile persists the CA certificate PEM to a temp file so the client
// constructors can exercise their real file-based trust anchor path
// (TASK-080).
func writeCAFile(t *testing.T, caInst *ca.CA) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(path, caInst.CertPEM(), 0o600))
	return path
}

// newTLSGatewayServer starts an httptest TLS server with a CA-signed server
// certificate, mirroring the production gateway listener. clientAuth and
// clientCAs may be nil for server-authentication-only tests (TASK-080).
func newTLSGatewayServer(t *testing.T, certPEM, keyPEM []byte, clientAuth tls.ClientAuthType, clientCAs *x509.CertPool) *httptest.Server {
	t.Helper()
	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   clientAuth,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2", "http/1.1"},
	}
	ts.EnableHTTP2 = true
	ts.StartTLS()
	// The server certificate carries DNS SANs only; point clients at
	// localhost so the name matches (same approach as gateway_mtls_test.go
	// newGatewayServer).
	ts.URL = strings.Replace(ts.URL, "127.0.0.1", "localhost", 1)
	t.Cleanup(ts.Close)
	return ts
}

// newClientKeyAndCSR generates an Ed25519 key and a CSR signed by that key,
// ready for ca.SignCSR (client identity issuance). Named distinctly from
// gateway_mtls_test.go's newAgentKeyAndCSR to avoid a package-level clash.
func newClientKeyAndCSR(t *testing.T) (keyPEM, csrPEM []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "agent"},
		DNSNames: []string{"agent"},
	}, priv)
	require.NoError(t, err)
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
}

// TestNewCAOnlyHTTPClient covers AC-080-02 for the gateway mode: the CA-only
// client must derive RootCAs from caCertPath, complete a TLS handshake
// against a CA-signed gateway, and present no client certificate. A client
// built from an unrelated CA must fail the handshake (proves RootCAs really
// takes effect) (TASK-080).
func TestNewCAOnlyHTTPClient(t *testing.T) {
	gatewayCA, err := ca.New(ca.Config{TTL: 24 * time.Hour})
	require.NoError(t, err)
	caPath := writeCAFile(t, gatewayCA)

	serverCertPEM, serverKeyPEM, err := gatewayCA.SignServerCert([]string{"localhost"})
	require.NoError(t, err)
	ts := newTLSGatewayServer(t, serverCertPEM, serverKeyPEM, tls.NoClientCert, nil)

	client, err := operator.NewCAOnlyHTTPClient(caPath)
	require.NoError(t, err)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok, "CA-only client must use a *http.Transport")
	require.NotNil(t, transport.TLSClientConfig)
	require.NotNil(t, transport.TLSClientConfig.RootCAs,
		"RootCAs must come from caCertPath")
	require.True(t, transport.TLSClientConfig.RootCAs.Equal(gatewayCA.CertPool()),
		"RootCAs must carry exactly the gateway CA certificate")
	require.Empty(t, transport.TLSClientConfig.Certificates,
		"CA-only client must not present a client certificate")
	require.Equal(t, uint16(tls.VersionTLS13), transport.TLSClientConfig.MinVersion)

	// Handshake against the CA-signed gateway succeeds: no
	// x509: certificate signed by unknown authority.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, http.NoBody)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Negative path: a client trusting an unrelated CA must fail the
	// handshake with the unknown-authority verification error.
	otherCA, err := ca.New(ca.Config{TTL: 24 * time.Hour})
	require.NoError(t, err)
	otherPath := writeCAFile(t, otherCA)
	badClient, err := operator.NewCAOnlyHTTPClient(otherPath)
	require.NoError(t, err)
	badReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, http.NoBody)
	require.NoError(t, err)
	badResp, err := badClient.Do(badReq)
	if badResp != nil {
		badResp.Body.Close()
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown authority")
}

// TestNewAgentTLSClient covers the agent-mode mTLS path (AC-080-02): the
// client presents the enrolled identity certificate and trusts the gateway
// CA, and completes a handshake against a gateway that requires and verifies
// client certificates. This guards the agent-mode reuse against regression
// (TASK-080).
func TestNewAgentTLSClient(t *testing.T) {
	gatewayCA, err := ca.New(ca.Config{TTL: 24 * time.Hour})
	require.NoError(t, err)
	caPath := writeCAFile(t, gatewayCA)

	serverCertPEM, serverKeyPEM, err := gatewayCA.SignServerCert([]string{"localhost"})
	require.NoError(t, err)
	ts := newTLSGatewayServer(t, serverCertPEM, serverKeyPEM, tls.RequireAndVerifyClientCert, gatewayCA.CertPool())

	keyPEM, csrPEM := newClientKeyAndCSR(t)
	csrBlock, _ := pem.Decode(csrPEM)
	require.NotNil(t, csrBlock)
	csr, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	require.NoError(t, err)
	issued, err := gatewayCA.SignCSR(csr, nil)
	require.NoError(t, err)

	client, err := operator.NewAgentTLSClient(string(issued.PEM), string(keyPEM), caPath)
	require.NoError(t, err)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.Len(t, transport.TLSClientConfig.Certificates, 1,
		"mTLS client must present the identity certificate")
	require.NotNil(t, transport.TLSClientConfig.RootCAs)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, http.NoBody)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// TestNewCAOnlyHTTPClientMissingCA: a missing CA file must fail the
// constructor loudly instead of falling back to the system pool (fail-closed
// trust, D4=A) (TASK-080).
func TestNewCAOnlyHTTPClientMissingCA(t *testing.T) {
	_, err := operator.NewCAOnlyHTTPClient(filepath.Join(t.TempDir(), "missing-ca.crt"))
	require.Error(t, err)
}

// TestNewCAOnlyHTTPClientRejectsMalformedCA: a corrupt CA file must fail the
// constructor (fail-closed trust, D4=A) (TASK-080).
func TestNewCAOnlyHTTPClientRejectsMalformedCA(t *testing.T) {
	path := writeTempFile(t, "malformed-ca.crt", "not a PEM certificate")
	client, err := operator.NewCAOnlyHTTPClient(path)
	require.Error(t, err)
	require.Nil(t, client)
}

// TestNewAgentTLSClientRejectsMalformedInputs covers the negative constructor
// matrix for the mTLS client: malformed CA/cert/key and a certificate/private
// key mismatch must all fail closed with a nil client (TASK-080, AC-080-02).
func TestNewAgentTLSClientRejectsMalformedInputs(t *testing.T) {
	gatewayCA, err := ca.New(ca.Config{TTL: 24 * time.Hour})
	require.NoError(t, err)
	caPath := writeCAFile(t, gatewayCA)

	keyPEM, csrPEM := newClientKeyAndCSR(t)
	csrBlock, _ := pem.Decode(csrPEM)
	require.NotNil(t, csrBlock)
	csr, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	require.NoError(t, err)
	issued, err := gatewayCA.SignCSR(csr, nil)
	require.NoError(t, err)

	otherKeyPEM, _ := newClientKeyAndCSR(t)

	tests := []struct {
		name       string
		certPEM    string
		keyPEM     string
		caCertPath string
	}{
		{
			name:       "malformed CA PEM",
			certPEM:    string(issued.PEM),
			keyPEM:     string(keyPEM),
			caCertPath: writeTempFile(t, "bad-ca.crt", "not a PEM certificate"),
		},
		{
			name:       "malformed identity certificate PEM",
			certPEM:    "not a PEM certificate",
			keyPEM:     string(keyPEM),
			caCertPath: caPath,
		},
		{
			name:       "malformed identity key PEM",
			certPEM:    string(issued.PEM),
			keyPEM:     "not a PEM key",
			caCertPath: caPath,
		},
		{
			name:       "certificate key mismatch",
			certPEM:    string(issued.PEM),
			keyPEM:     string(otherKeyPEM),
			caCertPath: caPath,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := operator.NewAgentTLSClient(tt.certPEM, tt.keyPEM, tt.caCertPath)
			require.Error(t, err)
			require.Nil(t, client)
		})
	}
}

// writeTempFile persists arbitrary content to a temp file for negative
// constructor inputs (TASK-080).
func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}
