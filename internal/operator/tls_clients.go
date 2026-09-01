package operator

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/ndzuki/release-manager/internal/operator/ca"
)

// NewAgentTLSClient builds the mTLS HTTP client for the agent runtime: the
// enrolled identity certificate as client credential and the gateway CA from
// caCertPath as trust anchor (ADR-017), TLS 1.3 minimum (TASK-080).
func NewAgentTLSClient(certPEM, keyPEM, caCertPath string) (*http.Client, error) {
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("load identity certificate: %w", err)
	}
	pool, err := ca.LoadCertPool(caCertPath)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			RootCAs:      pool,
			MinVersion:   tls.VersionTLS13,
		},
		ForceAttemptHTTP2: true,
	}}, nil
}

// NewCAOnlyHTTPClient builds a CA-only HTTP client for the gateway-mode
// operator: the gateway CA from caCertPath as the trust anchor and no client
// certificate (the gateway operator has no bootstrap identity). Mirrors
// newGatewayEnroller (internal/operator/bootstrap) and SessionClient
// (internal/operator/session_client.go). Per core/go/connect-rpc.md, Connect
// clients accept a custom http.Client/Transport and TLS verification is
// delegated to net/http (RootCAs), so this Transport fully governs the
// handshake against the gateway. CA load failures fail closed (D4=A).
func NewCAOnlyHTTPClient(caCertPath string) (*http.Client, error) {
	pool, err := ca.LoadCertPool(caCertPath)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS13,
		},
		ForceAttemptHTTP2: true,
	}}, nil
}
