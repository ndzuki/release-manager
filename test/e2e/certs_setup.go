//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// generateCerts creates CA and client certificates for mTLS testing
// using Go's native crypto libraries (no openssl dependency).
// Certificates are written to a temp directory.
// Returns paths and the SHA256 fingerprint of the client cert.
func generateCerts(ctx context.Context, customerID string) (caFile, certFile, keyFile string, fingerprint string, err error) {
	dir, err := os.MkdirTemp("", "e2e-certs-*")
	if err != nil {
		return "", "", "", "", fmt.Errorf("create certs temp dir: %w", err)
	}

	caKeyFile := filepath.Join(dir, "ca.key")
	caCrtFile := filepath.Join(dir, "ca.crt")
	cKeyFile := filepath.Join(dir, "tls.key")
	cCrtFile := filepath.Join(dir, "tls.crt")

	// --- Generate CA ---
	caKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return "", "", "", "", fmt.Errorf("generate CA key: %w", err)
	}
	caSerial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			Organization: []string{"Release Manager E2E"},
			CommonName:   "e2e-ca",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return "", "", "", "", fmt.Errorf("create CA cert: %w", err)
	}
	if err := writePEM(caCrtFile, "CERTIFICATE", caDER); err != nil {
		return "", "", "", "", err
	}
	if err := writePEM(caKeyFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(caKey)); err != nil {
		return "", "", "", "", err
	}

	// --- Generate client/server cert (single cert for both mTLS roles) ---
	certKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", "", "", fmt.Errorf("generate cert key: %w", err)
	}
	certSerial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))

	// Build SAN list for all hostnames gRPC might use for TLS SNI.
	dnsNames := []string{
		fmt.Sprintf("release-operator-%[1]s.release-operator-%[1]s", customerID),
		fmt.Sprintf("release-operator-%s", customerID),
		fmt.Sprintf("release-operator-%[1]s.release-operator-%[1]s.svc", customerID),
		fmt.Sprintf("release-operator-%[1]s.release-operator-%[1]s.svc.cluster.local", customerID),
		"localhost",
		customerID,
	}

	caCert, err := parseCertificate(caDER)
	if err != nil {
		return "", "", "", "", fmt.Errorf("parse CA cert: %w", err)
	}

	certTemplate := &x509.Certificate{
		SerialNumber: certSerial,
		Subject: pkix.Name{
			Organization: []string{"Customer"},
			CommonName:   customerID,
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:    dnsNames,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, certTemplate, caCert, &certKey.PublicKey, caKey)
	if err != nil {
		return "", "", "", "", fmt.Errorf("create cert: %w", err)
	}
	if err := writePEM(cCrtFile, "CERTIFICATE", certDER); err != nil {
		return "", "", "", "", err
	}
	if err := writePEM(cKeyFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(certKey)); err != nil {
		return "", "", "", "", err
	}

	// Calculate fingerprint
	fp, err := certFingerprint(cCrtFile)
	if err != nil {
		return "", "", "", "", fmt.Errorf("calculate fingerprint: %w", err)
	}

	return caCrtFile, cCrtFile, cKeyFile, fp, nil
}

// writePEM encodes data as PEM and writes to path.
func writePEM(path, blockType string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: data})
}

// parseCertificate parses a DER-encoded x509 certificate.
func parseCertificate(der []byte) (*x509.Certificate, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return cert, nil
}

// certFingerprint returns the SHA256 fingerprint of a certificate file.
func certFingerprint(certFile string) (string, error) {
	data, err := os.ReadFile(certFile)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", fmt.Errorf("failed to parse PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse certificate: %w", err)
	}
	h := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(h[:]), nil
}
