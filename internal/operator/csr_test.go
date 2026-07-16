package operator

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
)

// generateTestCSR creates a PEM-encoded CSR with the given CN and DNS SANs.
func generateTestCSR(t *testing.T, cn string, dnsNames []string) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: cn},
		DNSNames: dnsNames,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, priv)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestParseCSR(t *testing.T) {
	t.Run("valid CSR", func(t *testing.T) {
		csrPEM := generateTestCSR(t, "op-1", []string{"cust-1", "cluster-1"})
		csr, err := parseCSR(csrPEM)
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		if csr.Subject.CommonName != "op-1" {
			t.Fatalf("expected CN op-1, got %q", csr.Subject.CommonName)
		}
	})

	t.Run("invalid PEM", func(t *testing.T) {
		_, err := parseCSR([]byte("not-pem"))
		if err == nil {
			t.Fatal("expected error for invalid PEM")
		}
	})

	t.Run("wrong PEM type", func(t *testing.T) {
		block := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte{}})
		_, err := parseCSR(block)
		if err == nil {
			t.Fatal("expected error for wrong PEM type")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		_, err := parseCSR(nil)
		if err == nil {
			t.Fatal("expected error for nil input")
		}
	})
}

func TestValidateCSRSANs(t *testing.T) {
	tests := []struct {
		name       string
		cn         string
		dnsNames   []string
		customerID string
		clusterID  string
		wantErr    bool
	}{
		{
			name:       "both SANs present",
			cn:         "op-1",
			dnsNames:   []string{"cust-1", "cluster-1"},
			customerID: "cust-1",
			clusterID:  "cluster-1",
			wantErr:    false,
		},
		{
			name:       "customer_id in CN, cluster_id in DNS",
			cn:         "cust-1",
			dnsNames:   []string{"cluster-1"},
			customerID: "cust-1",
			clusterID:  "cluster-1",
			wantErr:    false,
		},
		{
			name:       "missing customer_id",
			cn:         "op-1",
			dnsNames:   []string{"cluster-1"},
			customerID: "cust-1",
			clusterID:  "cluster-1",
			wantErr:    true,
		},
		{
			name:       "missing cluster_id",
			cn:         "op-1",
			dnsNames:   []string{"cust-1"},
			customerID: "cust-1",
			clusterID:  "cluster-1",
			wantErr:    true,
		},
		{
			name:       "both missing",
			cn:         "op-1",
			dnsNames:   []string{},
			customerID: "cust-1",
			clusterID:  "cluster-1",
			wantErr:    true,
		},
		{
			name:       "empty customer_id check",
			cn:         "op-1",
			dnsNames:   []string{"cluster-1"},
			customerID: "",
			clusterID:  "cluster-1",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			csrPEM := generateTestCSR(t, tt.cn, tt.dnsNames)
			csr, err := parseCSR(csrPEM)
			if err != nil {
				t.Fatalf("parseCSR: %v", err)
			}
			err = validateCSRSANs(csr, tt.customerID, tt.clusterID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got err=%v", tt.wantErr, err)
			}
		})
	}
}
