package operator

import (
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
)

const maxDNSLabelLength = 63

type csrIdentity struct {
	OperatorName string
	DNSName      string
	CustomerID   string
	ClusterID    string
}

func parseCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, rest := pem.Decode(csrPEM)
	if block == nil || len(rest) != 0 {
		return nil, fmt.Errorf("decode CSR PEM")
	}
	if block.Type != "CERTIFICATE REQUEST" && block.Type != "NEW CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("unexpected CSR PEM block type")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("verify CSR signature: %w", err)
	}
	switch publicKey := csr.PublicKey.(type) {
	case ed25519.PublicKey:
		if len(publicKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid Ed25519 public key")
		}
	case *rsa.PublicKey:
		if publicKey.N == nil || publicKey.N.BitLen() < 2048 {
			return nil, fmt.Errorf("RSA public key must be at least 2048 bits")
		}
	default:
		return nil, fmt.Errorf("unsupported CSR public key algorithm")
	}
	return csr, nil
}

func validateCSRIdentity(csr *x509.CertificateRequest, customerID, clusterID string) (csrIdentity, error) {
	if csr == nil {
		return csrIdentity{}, fmt.Errorf("CSR is required")
	}
	operatorName := strings.TrimSpace(csr.Subject.CommonName)
	if err := validateDNSLabel(operatorName); err != nil {
		return csrIdentity{}, fmt.Errorf("invalid operator_name: %w", err)
	}
	customerID, err := normalizeDNSLabel(customerID)
	if err != nil {
		return csrIdentity{}, fmt.Errorf("invalid customer_id: %w", err)
	}
	clusterID, err = normalizeDNSLabel(clusterID)
	if err != nil {
		return csrIdentity{}, fmt.Errorf("invalid cluster_id: %w", err)
	}
	canonicalName := clusterID + "." + customerID + ".rm"
	matched := false
	for _, dnsName := range csr.DNSNames {
		if strings.EqualFold(strings.TrimSpace(dnsName), canonicalName) {
			matched = true
			break
		}
	}
	if !matched {
		return csrIdentity{}, fmt.Errorf("CSR SAN does not contain canonical operator identity")
	}
	return csrIdentity{
		OperatorName: operatorName,
		DNSName:      canonicalName,
		CustomerID:   customerID,
		ClusterID:    clusterID,
	}, nil
}

func normalizeDNSLabel(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if err := validateDNSLabel(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

func validateDNSLabel(value string) error {
	if value == "" || len(value) > maxDNSLabelLength {
		return fmt.Errorf("DNS label length must be 1-63")
	}
	if value[0] == '-' || value[len(value)-1] == '-' {
		return fmt.Errorf("DNS label cannot start or end with hyphen")
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return fmt.Errorf("DNS label contains invalid character")
	}
	return nil
}
