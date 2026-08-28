package operator

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func generateTestCSR(t *testing.T, cn string, dnsNames []string) []byte {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return createCSR(t, cn, dnsNames, privateKey)
}

func createCSR(t *testing.T, cn string, dnsNames []string, privateKey any) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: cn},
		DNSNames: dnsNames,
	}, privateKey)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestParseCSR(t *testing.T) {
	tests := []struct {
		name    string
		csrPEM  func(*testing.T) []byte
		wantErr bool
	}{
		{
			name: "valid ed25519",
			csrPEM: func(t *testing.T) []byte {
				return generateTestCSR(t, "operator-1", []string{"cluster-1.customer-1.rm"})
			},
		},
		{
			name: "valid rsa 2048",
			csrPEM: func(t *testing.T) []byte {
				key, err := rsa.GenerateKey(rand.Reader, 2048)
				require.NoError(t, err)
				return createCSR(t, "operator-1", []string{"cluster-1.customer-1.rm"}, key)
			},
		},
		{
			name: "rsa below 2048",
			csrPEM: func(t *testing.T) []byte {
				key, err := rsa.GenerateKey(rand.Reader, 1024)
				require.NoError(t, err)
				return createCSR(t, "operator-1", []string{"cluster-1.customer-1.rm"}, key)
			},
			wantErr: true,
		},
		{
			name:    "invalid pem",
			csrPEM:  func(*testing.T) []byte { return []byte("not-pem") },
			wantErr: true,
		},
		{
			name: "wrong pem type",
			csrPEM: func(*testing.T) []byte {
				return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("bad")})
			},
			wantErr: true,
		},
		{
			name:    "empty input",
			csrPEM:  func(*testing.T) []byte { return nil },
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCSR(test.csrPEM(t))
			assert.Equal(t, test.wantErr, err != nil)
		})
	}
}

func TestParseCSRRejectsInvalidSignature(t *testing.T) {
	csrPEM := generateTestCSR(t, "operator-1", []string{"cluster-1.customer-1.rm"})
	block, rest := pem.Decode(csrPEM)
	require.Empty(t, rest)
	require.NotNil(t, block)
	block.Bytes[len(block.Bytes)-1] ^= 0xff

	_, err := parseCSR(pem.EncodeToMemory(block))
	require.Error(t, err)
}

func TestValidateCSRIdentity(t *testing.T) {
	tests := []struct {
		name       string
		cn         string
		dnsNames   []string
		customerID string
		clusterID  string
		want       csrIdentity
		wantErr    bool
	}{
		{
			name: "canonical",
			cn:   "operator-1", dnsNames: []string{"cluster-1.customer-1.rm"},
			customerID: "customer-1", clusterID: "cluster-1",
			want: csrIdentity{OperatorName: "operator-1", DNSName: "cluster-1.customer-1.rm", CustomerID: "customer-1", ClusterID: "cluster-1"},
		},
		{
			name: "uppercase scope normalizes",
			cn:   "operator-1", dnsNames: []string{"CLUSTER-1.CUSTOMER-1.RM"},
			customerID: "CUSTOMER-1", clusterID: "CLUSTER-1",
			want: csrIdentity{OperatorName: "operator-1", DNSName: "cluster-1.customer-1.rm", CustomerID: "customer-1", ClusterID: "cluster-1"},
		},
		{
			name: "missing canonical san",
			cn:   "operator-1", dnsNames: []string{"cluster-1", "customer-1"},
			customerID: "customer-1", clusterID: "cluster-1", wantErr: true,
		},
		{
			name: "customer mismatch",
			cn:   "operator-1", dnsNames: []string{"cluster-1.other-customer.rm"},
			customerID: "customer-1", clusterID: "cluster-1", wantErr: true,
		},
		{
			name: "invalid operator name",
			cn:   "Operator_1", dnsNames: []string{"cluster-1.customer-1.rm"},
			customerID: "customer-1", clusterID: "cluster-1", wantErr: true,
		},
		{
			name: "empty customer id",
			cn:   "operator-1", dnsNames: []string{"cluster-1.customer-1.rm"},
			customerID: "", clusterID: "cluster-1", wantErr: true,
		},
		{
			name: "invalid cluster label",
			cn:   "operator-1", dnsNames: []string{"cluster_1.customer-1.rm"},
			customerID: "customer-1", clusterID: "cluster_1", wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			csr, err := parseCSR(generateTestCSR(t, test.cn, test.dnsNames))
			require.NoError(t, err)
			identity, err := validateCSRIdentity(csr, test.customerID, test.clusterID)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, identity)
		})
	}
}
