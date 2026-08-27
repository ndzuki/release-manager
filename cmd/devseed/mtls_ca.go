package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	operatorca "github.com/ndzuki/release-manager/internal/operator/ca"
)

// ensureDevMTLSCA implements AC-065-36 (REQ-065 批次5 D1): ensure the dev
// mTLS CA pair (ca.key + ca.crt) exists in dir. The contract is exactly the
// one the operator gateway's ca.Load consumes — a parseable existing pair is
// reused untouched; a missing or corrupt pair is regenerated. The PEM format
// mirrors internal/operator/ca.New: PKCS#8 Ed25519 private key ("PRIVATE
// KEY") plus a self-signed CA certificate (IsCA, CertSign|CRLSign), so
// dev-up can never produce a CA the orchestrator refuses to load.
func ensureDevMTLSCA(dir string) error {
	if dir == "" {
		return fmt.Errorf("mtls-ca-dir is required")
	}
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.crt")

	reusable, reuseErr := devCAReusable(keyPath, certPath)
	if reusable {
		fmt.Printf("dev mTLS CA reused from %s\n", dir)
		return nil
	}
	if err := generateDevMTLSCA(dir, keyPath, certPath); err != nil {
		return err
	}
	if reuseErr != nil {
		fmt.Fprintf(os.Stderr, "existing dev CA pair regenerated (was: %v)\n", reuseErr)
	}
	fmt.Printf("dev mTLS CA generated in %s\n", dir)
	return nil
}

// devCAReusable reports whether both files exist and parse as a valid CA
// pair (checked with the operator's own loader).
func devCAReusable(keyPath, certPath string) (bool, error) {
	keyPEM, keyErr := os.ReadFile(keyPath)
	certPEM, certErr := os.ReadFile(certPath)
	if keyErr != nil || certErr != nil {
		return false, fmt.Errorf("CA pair incomplete (key=%v cert=%v)", keyErr, certErr)
	}
	if _, err := operatorca.Load(keyPEM, certPEM, operatorca.Config{}); err != nil {
		return false, fmt.Errorf("existing CA pair unparseable: %w", err)
	}
	return true, nil
}

// generateDevMTLSCA creates a fresh Ed25519 self-signed CA and persists both
// files atomically (0600; the directory is created 0700). The generated
// bytes are validated through the operator loader before anything is
// written, so a format mismatch fails the helper instead of the gateway.
func generateDevMTLSCA(dir, keyPath, certPath string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate CA serial: %w", err)
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "release-manager-dev-ca"},
		// The dev CA is a long-lived trust anchor (rotation = delete
		// data/dev-ca/ + re-run dev-up), unlike the 7-day client certs the
		// gateway issues from it.
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return fmt.Errorf("create CA certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal CA key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	// Validate through the exact loader the orchestrator gateway uses; a
	// self-signature or key/cert mismatch must fail here, not at boot.
	if _, err := operatorca.Load(keyPEM, certPEM, operatorca.Config{}); err != nil {
		return fmt.Errorf("generated CA pair fails the operator loader: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create CA dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod CA dir: %w", err)
	}
	if err := writeFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write CA key: %w", err)
	}
	if err := writeFileAtomic(certPath, certPEM, 0o600); err != nil {
		return fmt.Errorf("write CA cert: %w", err)
	}
	return nil
}

// writeFileAtomic persists data via a temp file + rename so a crash never
// leaves a truncated CA file behind (the reuse path would treat it as
// corrupt and regenerate — but never serve half-written bytes).
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best-effort temp cleanup
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck // the write error is the primary failure; close is best-effort
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close() //nolint:errcheck // the chmod error is the primary failure; close is best-effort
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
