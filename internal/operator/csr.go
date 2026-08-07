package operator

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
)

// parseCSR parses a PEM-encoded certificate signing request.
func parseCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode CSR PEM")
	}
	if block.Type != "CERTIFICATE REQUEST" && block.Type != "NEW CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("unexpected PEM block type: %q", block.Type)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CSR: %w", err)
	}
	return csr, nil
}

// validateCSRSANs checks that the CSR SANs contain both customerID and clusterID.
// Returns an error if either is missing — AC-015-06.
func validateCSRSANs(csr *x509.CertificateRequest, customerID, clusterID string) error {
	dnsNames := csr.DNSNames
	// Also check Subject.CommonName as a SAN fallback per convention.
	if csr.Subject.CommonName != "" {
		dnsNames = append(dnsNames, csr.Subject.CommonName)
	}

	hasCustomer := false
	hasCluster := false
	for _, name := range dnsNames {
		if strings.EqualFold(name, customerID) {
			hasCustomer = true
		}
		if strings.EqualFold(name, clusterID) {
			hasCluster = true
		}
	}

	var missing []string
	if !hasCustomer {
		missing = append(missing, fmt.Sprintf("customer_id=%q", customerID))
	}
	if !hasCluster {
		missing = append(missing, fmt.Sprintf("cluster_id=%q", clusterID))
	}
	if len(missing) > 0 {
		return fmt.Errorf("CSR SANs missing required identifiers: %s", strings.Join(missing, ", "))
	}
	return nil
}
