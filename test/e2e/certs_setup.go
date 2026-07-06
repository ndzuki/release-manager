//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// generateCerts creates CA and client certificates for mTLS testing.
// Certificates are written to a temp directory.
// Returns paths and the SHA256 fingerprint of the client cert.
func generateCerts(ctx context.Context, customerID string) (caFile, certFile, keyFile string, fingerprint string, err error) {
	dir, err := os.MkdirTemp("", "e2e-certs-*")
	if err != nil {
		return "", "", "", "", fmt.Errorf("create certs temp dir: %w", err)
	}

	caKey := filepath.Join(dir, "ca.key")
	caCrt := filepath.Join(dir, "ca.crt")
	cKey := filepath.Join(dir, "tls.key")
	cCsr := filepath.Join(dir, "tls.csr")
	cCrt := filepath.Join(dir, "tls.crt")

	// Generate CA
	run := func(name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w\n%s", name, err, string(out))
		}
		return nil
	}

	if err := run("openssl", "genrsa", "-out", caKey, "4096"); err != nil {
		return "", "", "", "", err
	}
	if err := run("openssl", "req", "-x509", "-new", "-nodes",
		"-key", caKey, "-sha256", "-days", "365",
		"-subj", "/O=Release Manager E2E/CN=e2e-ca",
		"-out", caCrt); err != nil {
		return "", "", "", "", err
	}

	// Generate client cert
	if err := run("openssl", "genrsa", "-out", cKey, "2048"); err != nil {
		return "", "", "", "", err
	}
	if err := run("openssl", "req", "-new", "-key", cKey,
		"-subj", fmt.Sprintf("/O=Customer/CN=%s", customerID),
		"-out", cCsr); err != nil {
		return "", "", "", "", err
	}

	// Write SAN extension file for TLS hostname verification.
	// Manager dials operator at its service DNS: release-operator-{id}.release-operator-{id}
	sanFile := filepath.Join(dir, "san.cnf")
	// Add SANs for all possible hostnames gRPC might use for TLS SNI:
	// - Full K8s service DNS: release-operator-{id}.release-operator-{id}
	// - Short service name: release-operator-{id}
	// - Service DNS with svc suffix: release-operator-{id}.release-operator-{id}.svc
	// - localhost for port-forward scenarios
	// - CN (customerID) as fallback
	// Add SANs + key usage extensions required by Go TLS for server/client auth.
	sanContent := fmt.Sprintf("subjectAltName=DNS:release-operator-%[1]s.release-operator-%[1]s,DNS:release-operator-%[1]s,DNS:release-operator-%[1]s.release-operator-%[1]s.svc,DNS:release-operator-%[1]s.release-operator-%[1]s.svc.cluster.local,DNS:localhost,DNS:%[1]s\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth,clientAuth", customerID)
	if err := os.WriteFile(sanFile, []byte(sanContent), 0o644); err != nil {
		return "", "", "", "", fmt.Errorf("write SAN extfile: %w", err)
	}
	if err := run("openssl", "x509", "-req", "-in", cCsr,
		"-CA", caCrt, "-CAkey", caKey, "-CAcreateserial",
		"-out", cCrt, "-days", "365", "-sha256",
		"-extfile", sanFile); err != nil {
		return "", "", "", "", err
	}

	// Calculate fingerprint
	fp, err := certFingerprint(cCrt)
	if err != nil {
		return "", "", "", "", fmt.Errorf("calculate fingerprint: %w", err)
	}

	// Cleanup CSR
	os.Remove(cCsr)

	return caCrt, cCrt, cKey, fp, nil
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
