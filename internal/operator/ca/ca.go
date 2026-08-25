// Package ca provides the persistent self-signed certificate authority for
// operator mTLS (ADR-017/ADR-018).
package ca

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"
)

// IssuedCertificate is the product of signing a CSR: the DER certificate, its
// PEM form, the ADR-018 serial authority (sha256(DER)[:10] hex) and expiry.
type IssuedCertificate struct {
	DER       []byte
	PEM       []byte
	Serial    string
	ExpiresAt time.Time
}

// CA is a self-signed certificate authority for signing operator CSRs.
type CA struct {
	cert   *x509.Certificate
	priv   ed25519.PrivateKey
	ttl    time.Duration
	serial *big.Int
}

// Config holds CA creation parameters.
type Config struct {
	// TTL is the validity duration for signed certificates.
	// Defaults to 7 days if zero.
	TTL time.Duration
}

// New creates a new self-signed CA with an Ed25519 key pair.
func New(cfg Config) (*CA, error) {
	if cfg.TTL == 0 {
		cfg.TTL = 7 * 24 * time.Hour
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("generate CA serial: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "release-manager CA",
		},
		NotBefore:             time.Now().UTC(),
		NotAfter:              time.Now().UTC().Add(cfg.TTL * 10), // CA lives 10x longer
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true, // no intermediate CAs
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	return &CA{
		cert:   cert,
		priv:   priv,
		ttl:    cfg.TTL,
		serial: serial,
	}, nil
}

// LoadOrCreate loads the CA from keyPath/certPath, generating and atomically
// persisting a fresh CA when either file is missing (TASK-075 gateway wiring:
// a stable CA across restarts keeps the agent trust chain intact — a new CA on
// every start would invalidate every enrolled agent certificate). File-based
// convenience wrapper over LoadOrCreateWithProvider.
func LoadOrCreate(cfg Config, keyPath, certPath string) (*CA, error) {
	return LoadOrCreateWithProvider(context.Background(), NewFileProvider(certPath, keyPath), cfg)
}

// LoadOrCreateWithProvider loads CA credentials through the provider and
// generates + persists a fresh CA when none exist (ADR-017). A racing creator
// re-loads the persisted credentials instead of overwriting them, so every
// process converges on the same trust anchor.
func LoadOrCreateWithProvider(ctx context.Context, provider Provider, cfg Config) (*CA, error) {
	if provider == nil {
		return nil, fmt.Errorf("load CA: provider is required")
	}
	if cfg.TTL == 0 {
		cfg.TTL = 7 * 24 * time.Hour
	}
	credentials, err := provider.Load(ctx)
	if err == nil {
		return Load(credentials.PrivateKeyPEM, credentials.CertificatePEM, cfg)
	}
	if !errors.Is(err, ErrCredentialsNotFound) {
		return nil, fmt.Errorf("load CA credentials: %w", err)
	}
	generated, genErr := New(cfg)
	if genErr != nil {
		return nil, genErr
	}
	keyDER, marshalErr := x509.MarshalPKCS8PrivateKey(generated.priv)
	if marshalErr != nil {
		return nil, fmt.Errorf("marshal CA key: %w", marshalErr)
	}
	toStore := Credentials{
		CertificatePEM: generated.CertPEM(),
		PrivateKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
	if createErr := provider.Create(ctx, toStore); createErr != nil {
		if loaded, loadErr := provider.Load(ctx); loadErr == nil {
			return Load(loaded.PrivateKeyPEM, loaded.CertificatePEM, cfg)
		}
		return nil, fmt.Errorf("persist CA credentials: %w", errors.Join(createErr))
	}
	return generated, nil
}

// Load parses a CA from PEM-encoded key and certificate material. The key must
// be an Ed25519 PKCS#8 private key; the certificate must be a self-signed CA
// certificate matching the key.
func Load(keyPEM, certPEM []byte, cfg Config) (*CA, error) {
	if cfg.TTL == 0 {
		cfg.TTL = 7 * 24 * time.Hour
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("decode CA private key: invalid PEM block")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA private key: %w", err)
	}
	priv, ok := keyAny.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("CA private key must be Ed25519, got %T", keyAny)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("decode CA certificate: invalid PEM block")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("CA certificate is not a CA certificate")
	}
	// The self-signature proves the certificate is intact; a separate check
	// proves the loaded private key actually matches the certificate public
	// key (a mismatched pair would silently sign certificates the trust
	// anchor does not back).
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok || !pub.Equal(cert.PublicKey) {
		return nil, fmt.Errorf("CA private key does not match certificate")
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		return nil, fmt.Errorf("CA certificate signature does not match key: %w", err)
	}
	serial := new(big.Int).Set(cert.SerialNumber)
	return &CA{cert: cert, priv: priv, ttl: cfg.TTL, serial: serial}, nil
}

// LoadCertPool reads a PEM CA certificate file into a CertPool, the shared
// trust anchor for gateway clients and the gateway listener's ClientCAs.
func LoadCertPool(certPath string) (*x509.CertPool, error) {
	caPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read gateway CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("gateway CA file contains no certificates")
	}
	return pool, nil
}

// SignCSR signs a certificate signing request and returns the issued
// certificate (REQ-015 证书契约): ExtKeyUsage=clientAuth, KeyUsage=
// digitalSignature only, TTL=ca.cert_ttl, serial=ADR-018 sha256(DER)[:10]
// hex. dnsNames is the authoritative (normalized) SAN set — it overrides the
// raw CSR SANs so the issued certificate carries exactly the canonical
// operator identity. The CSR signature proves private-key possession.
func (ca *CA) SignCSR(csr *x509.CertificateRequest, dnsNames []string) (*IssuedCertificate, error) {
	if csr == nil {
		return nil, fmt.Errorf("sign CSR: CSR is required")
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("sign CSR: invalid signature: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now().UTC()
	expiresAt := now.Add(ca.ttl)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      csr.Subject,
		DNSNames:     append([]string(nil), dnsNames...),
		NotBefore:    now,
		NotAfter:     expiresAt,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.priv)
	if err != nil {
		return nil, fmt.Errorf("sign CSR: %w", err)
	}
	return &IssuedCertificate{
		DER:       der,
		PEM:       CertDERToPEM(der),
		Serial:    CertSerialDER(der),
		ExpiresAt: expiresAt,
	}, nil
}

// CertPEM returns the CA certificate in PEM format.
func (ca *CA) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw})
}

// TTL returns the configured certificate validity duration.
func (ca *CA) TTL() time.Duration { return ca.ttl }

// SignServerCert issues a server certificate for the given hostnames, signed by
// this CA with the serverAuth EKU. It returns the certificate and private key
// in PEM form for the gateway TLS listener (TASK-075 gateway wiring).
func (ca *CA) SignServerCert(hostnames []string) (certPEM, keyPEM []byte, err error) {
	if len(hostnames) == 0 {
		return nil, nil, fmt.Errorf("server hostnames are required")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate server key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, fmt.Errorf("generate server serial: %w", err)
	}

	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: hostnames[0],
		},
		DNSNames:    hostnames,
		NotBefore:   now,
		NotAfter:    now.Add(ca.ttl),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.priv)
	if err != nil {
		return nil, nil, fmt.Errorf("sign server certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal server key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// CertPool returns a pool containing the CA certificate, used as the gateway
// ClientCAs trust anchor for client certificate verification.
func (ca *CA) CertPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	return pool
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	return serial, nil
}

// CertDERToPEM encodes a DER certificate to PEM format.
func CertDERToPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// CertSerialDER is the ADR-018 identity authority: sha256(certDER)[:10] hex.
func CertSerialDER(der []byte) string {
	digest := sha256.Sum256(der)
	return hex.EncodeToString(digest[:10])
}

// CertSerial returns the ADR-018 serial for a parsed certificate.
func CertSerial(certificate *x509.Certificate) string {
	if certificate == nil {
		return ""
	}
	return CertSerialDER(certificate.Raw)
}
