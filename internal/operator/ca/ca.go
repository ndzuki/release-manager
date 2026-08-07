// Package ca provides a self-signed certificate authority for operator mTLS.
package ca

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

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

// SignCSR signs a certificate signing request and returns the DER-encoded
// certificate. The returned certificate is valid for the CA's configured TTL.
func (ca *CA) SignCSR(csr *x509.CertificateRequest) ([]byte, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      csr.Subject,
		DNSNames:     csr.DNSNames,
		NotBefore:    now,
		NotAfter:     now.Add(ca.ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.priv)
	if err != nil {
		return nil, fmt.Errorf("sign CSR: %w", err)
	}
	return der, nil
}

// CertPEM returns the CA certificate in PEM format.
func (ca *CA) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw})
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
