package ca_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/operator/ca"
)

// TestLoadOrCreateRoundTrip: first call generates and persists the CA, a
// second call must load the same identity (TASK-075 gateway restart keeps the
// agent trust chain stable — AC-075-01/02 precondition).
func TestLoadOrCreateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.crt")

	first, err := ca.LoadOrCreate(ca.Config{TTL: 24 * time.Hour}, keyPath, certPath)
	require.NoError(t, err)

	// Files exist with the expected permissions (key 0600, cert 0644).
	keyInfo, err := os.Stat(keyPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())
	certInfo, err := os.Stat(certPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), certInfo.Mode().Perm())

	// Second load must reproduce the same CA certificate (identity stability).
	second, err := ca.LoadOrCreate(ca.Config{TTL: 24 * time.Hour}, keyPath, certPath)
	require.NoError(t, err)
	require.Equal(t, string(first.CertPEM()), string(second.CertPEM()))
}

// TestLoadOrCreatePartialMissing: a missing key file (e.g. deleted state) must
// regenerate the pair instead of loading a half CA.
func TestLoadOrCreatePartialMissing(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.crt")

	first, err := ca.LoadOrCreate(ca.Config{}, keyPath, certPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(keyPath))

	second, err := ca.LoadOrCreate(ca.Config{}, keyPath, certPath)
	require.NoError(t, err)
	require.NotEqual(t, string(first.CertPEM()), string(second.CertPEM()),
		"regenerated CA must differ from the deleted one")
}

// TestLoadOrCreateEmptyPaths: missing paths are a configuration error, not a
// silent generation in an undefined location.
func TestLoadOrCreateEmptyPaths(t *testing.T) {
	_, err := ca.LoadOrCreate(ca.Config{}, "", "")
	require.Error(t, err)
}

// TestLoadRejectsCorruptFiles: a corrupt key or certificate must fail loudly
// instead of silently replacing the persisted CA.
func TestLoadRejectsCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.crt")
	_, err := ca.LoadOrCreate(ca.Config{}, keyPath, certPath)
	require.NoError(t, err)

	// Corrupt key: Load must reject it.
	keyPEM, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	goodCert, err := os.ReadFile(certPath)
	require.NoError(t, err)
	_, err = ca.Load([]byte("not a pem"), goodCert, ca.Config{})
	require.Error(t, err)

	// Corrupt certificate: Load must reject it.
	_, err = ca.Load(keyPEM, []byte("garbage"), ca.Config{})
	require.Error(t, err)
}

// TestLoadRejectsMismatchedKeyAndCertificate: each file may be valid on its
// own, but a partial restore from different CA generations must fail. Loading
// such a pair would sign certificates the trust anchor cannot verify.
func TestLoadRejectsMismatchedKeyAndCertificate(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	firstKeyPath := filepath.Join(firstDir, "ca.key")
	firstCertPath := filepath.Join(firstDir, "ca.crt")
	secondKeyPath := filepath.Join(secondDir, "ca.key")
	secondCertPath := filepath.Join(secondDir, "ca.crt")

	_, err := ca.LoadOrCreate(ca.Config{}, firstKeyPath, firstCertPath)
	require.NoError(t, err)
	_, err = ca.LoadOrCreate(ca.Config{}, secondKeyPath, secondCertPath)
	require.NoError(t, err)

	firstKey, err := os.ReadFile(firstKeyPath)
	require.NoError(t, err)
	secondCert, err := os.ReadFile(secondCertPath)
	require.NoError(t, err)
	_, err = ca.Load(firstKey, secondCert, ca.Config{})
	require.ErrorContains(t, err, "private key does not match certificate")
}

// parseServerCert decodes the PEM certificate SignServerCert* returns, the same
// bytes the gateway hands to tls.X509KeyPair.
func parseServerCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block, "server certificate must be a PEM CERTIFICATE block")
	require.Equal(t, "CERTIFICATE", block.Type)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

// TASK-388: the gateway leaf must carry the IP SAN a customer agent checks.
// The decisive assertion is the one a client makes: x509.Verify with the IP as
// DNSName succeeds, and an address that is not a SAN still fails. A certificate
// built from DNS-only SANs cannot pass this test.
func TestSignServerCertWithSANs(t *testing.T) {
	dir := t.TempDir()
	caInst, err := ca.LoadOrCreate(ca.Config{TTL: 24 * time.Hour},
		filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.crt"))
	require.NoError(t, err)

	hostnames := []string{"rm-prod.local", "localhost"}
	ips := []net.IP{net.ParseIP("10.10.33.122"), net.ParseIP("2001:db8::1")}
	certPEM, keyPEM, err := caInst.SignServerCertWithSANs(hostnames, ips)
	require.NoError(t, err)

	// The returned pair must be usable as a TLS server certificate.
	_, err = tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err, "certificate and key must form a pair")

	cert := parseServerCert(t, certPEM)
	require.Equal(t, hostnames, cert.DNSNames)
	require.Len(t, cert.IPAddresses, len(ips))
	for i, want := range ips {
		require.Truef(t, want.Equal(cert.IPAddresses[i]),
			"IP SAN %d = %s, want %s", i, cert.IPAddresses[i], want)
	}
	require.Equal(t, "rm-prod.local", cert.Subject.CommonName)
	require.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, cert.ExtKeyUsage)
	require.Equal(t, x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment, cert.KeyUsage)
	require.WithinDuration(t, time.Now().Add(24*time.Hour), cert.NotAfter, 10*time.Second)
	require.WithinDuration(t, time.Now(), cert.NotBefore, 10*time.Second)

	// The customer scenario: an agent talking to https://10.10.33.122:8444.
	_, err = cert.Verify(x509.VerifyOptions{DNSName: "10.10.33.122", Roots: caInst.CertPool()})
	require.NoError(t, err, "an agent connecting by IP must accept the gateway certificate")
	_, err = cert.Verify(x509.VerifyOptions{DNSName: "rm-prod.local", Roots: caInst.CertPool()})
	require.NoError(t, err, "the DNS SAN must keep verifying")
	_, err = cert.Verify(x509.VerifyOptions{DNSName: "172.16.0.9", Roots: caInst.CertPool()})
	require.Error(t, err, "an address that is not a SAN must still be rejected")
}

// TASK-388: with no hostname the common name falls back to the first IP, so an
// IP-only deployment still gets a well-formed certificate.
func TestSignServerCertWithSANsIPOnlyCommonName(t *testing.T) {
	dir := t.TempDir()
	caInst, err := ca.LoadOrCreate(ca.Config{}, filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.crt"))
	require.NoError(t, err)

	certPEM, _, err := caInst.SignServerCertWithSANs(nil, []net.IP{net.ParseIP("10.10.33.122")})
	require.NoError(t, err)

	cert := parseServerCert(t, certPEM)
	require.Empty(t, cert.DNSNames)
	require.Equal(t, "10.10.33.122", cert.Subject.CommonName)
	require.Len(t, cert.IPAddresses, 1)
	require.True(t, net.ParseIP("10.10.33.122").Equal(cert.IPAddresses[0]))
}

// TASK-388: a certificate with no SAN at all can never be verified, so empty
// input must be an error rather than an unusable certificate.
func TestSignServerCertWithSANsRejectsEmptyInput(t *testing.T) {
	dir := t.TempDir()
	caInst, err := ca.LoadOrCreate(ca.Config{}, filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.crt"))
	require.NoError(t, err)

	_, _, err = caInst.SignServerCertWithSANs(nil, nil)
	require.ErrorContains(t, err, "hostnames or IP addresses are required")
}

// The pre-TASK-388 entry point keeps its signature and its certificate shape:
// DNS SANs only, no IP SAN, common name = first hostname.
func TestSignServerCertKeepsLegacyShape(t *testing.T) {
	dir := t.TempDir()
	caInst, err := ca.LoadOrCreate(ca.Config{}, filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.crt"))
	require.NoError(t, err)

	certPEM, _, err := caInst.SignServerCert([]string{"operator-gateway.dev.release-manager.local", "localhost"})
	require.NoError(t, err)
	cert := parseServerCert(t, certPEM)
	require.Equal(t, []string{"operator-gateway.dev.release-manager.local", "localhost"}, cert.DNSNames)
	require.Empty(t, cert.IPAddresses)
	require.Equal(t, "operator-gateway.dev.release-manager.local", cert.Subject.CommonName)

	_, _, err = caInst.SignServerCert(nil)
	require.ErrorContains(t, err, "server hostnames are required")
}

// TASK-388: configuration carries SANs as strings, so the config-facing entry
// point must parse them and fail closed on an entry that is not an IP address —
// silently dropping it would ship exactly the DNS-only certificate this task
// exists to fix.
func TestSignServerCertWithIPSANsParsesConfiguredAddresses(t *testing.T) {
	dir := t.TempDir()
	caInst, err := ca.LoadOrCreate(ca.Config{}, filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.crt"))
	require.NoError(t, err)

	certPEM, _, err := caInst.SignServerCertWithIPSANs(
		[]string{"rm-prod.local"},
		[]string{"10.10.33.122", "2001:db8::1"},
	)
	require.NoError(t, err)
	cert := parseServerCert(t, certPEM)
	require.Equal(t, []string{"rm-prod.local"}, cert.DNSNames)
	require.Len(t, cert.IPAddresses, 2)
	require.Equal(t, "10.10.33.122", cert.IPAddresses[0].String())
	require.Equal(t, "2001:db8::1", cert.IPAddresses[1].String())

	_, _, err = caInst.SignServerCertWithIPSANs(nil, []string{"10.10.33.999"})
	require.ErrorContains(t, err, "is not an IP address")
}
