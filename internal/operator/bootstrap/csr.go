package bootstrap

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"strings"
)

// operatorName returns the CSR CommonName: the operator name, defaulting to
// the cluster ID when not configured (plan v1 Step 6).
func operatorName(cfg Config) string {
	if cfg.OperatorName != "" {
		return cfg.OperatorName
	}
	return cfg.ClusterID
}

// newKeyAndCSR generates an Ed25519 key pair and a CSR carrying the identity
// the gateway validates (REQ-015 AC-015-06, plan v1 Step 6): the canonical
// SAN `<cluster>.<customer>.rm` plus the flat customer/cluster identifiers
// the current main CSR validator requires.
func newKeyAndCSR(customerID, clusterID, operatorName string) (keyPEM, csrPEM []byte, err error) {
	if customerID == "" {
		return nil, nil, fmt.Errorf("customer id is required")
	}
	if clusterID == "" {
		return nil, nil, fmt.Errorf("cluster id is required")
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate identity key: %w", err)
	}
	canonicalSAN := strings.ToLower(clusterID + "." + customerID + ".rm")
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: operatorName},
		DNSNames: []string{
			strings.ToLower(customerID),
			strings.ToLower(clusterID),
			canonicalSAN,
		},
	}, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("create CSR: %w", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal identity key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}), nil
}
