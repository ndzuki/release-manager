package jwtauth

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// Access tokens are signed with EdDSA (Ed25519) — REQ-065 AC-065-01 / decision
// D1=A. The signing key stays with the issuer (cmd/auth); every verifier holds
// only the public half. That is the whole point of the asymmetric scheme: with
// the previous HS256 tokens every verifier needed the signing secret, so a
// compromised verifier (orchestrator, audit API) could mint arbitrary
// user/organization/role identities, and the secret had to be mounted into
// deployments that never used it.
//
// The parsers live in this package (not internal/auth) so the verify-only
// services do not have to link the auth service for key material.
//
// These parsers are the only place the key material enters the process, so they
// fail closed: a wrong key type, a malformed document, or a private key handed
// to a verifier is an error at startup, never a silently weaker configuration.

// ParseEd25519PrivateKeyPEM parses a single PKCS#8 Ed25519 private key from a
// PEM document (the format cmd/devseed's key generator emits: block type
// "PRIVATE KEY").
func ParseEd25519PrivateKeyPEM(keyPEM string) (ed25519.PrivateKey, error) {
	block, err := singlePEMBlock(keyPEM, "jwt private key")
	if err != nil {
		return nil, err
	}
	if strings.Contains(block.Type, "PUBLIC") {
		return nil, errors.New("jwt private key is a public key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse jwt private key: %w", err)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("jwt private key must be Ed25519, got %T", parsed)
	}
	return privateKey, nil
}

// ParseEd25519PublicKeyPEM parses a single PKIX Ed25519 public key from a PEM
// document (block type "PUBLIC KEY").
func ParseEd25519PublicKeyPEM(keyPEM string) (ed25519.PublicKey, error) {
	block, err := singlePEMBlock(keyPEM, "jwt public key")
	if err != nil {
		return nil, err
	}
	if strings.Contains(block.Type, "PRIVATE") {
		// A verifier must never be handed signing material: accepting it would
		// silently restore the ability to mint tokens.
		return nil, errors.New("jwt public key is a private key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse jwt public key: %w", err)
	}
	publicKey, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("jwt public key must be Ed25519, got %T", parsed)
	}
	return publicKey, nil
}

// singlePEMBlock decodes exactly one PEM block, rejecting empty input and
// trailing garbage so a mis-mounted Secret cannot half-configure a service.
func singlePEMBlock(keyPEM, what string) (*pem.Block, error) {
	trimmed := strings.TrimSpace(keyPEM)
	if trimmed == "" {
		return nil, fmt.Errorf("%s is empty", what)
	}
	block, rest := pem.Decode([]byte(trimmed))
	if block == nil {
		return nil, fmt.Errorf("%s must be a PEM document", what)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, fmt.Errorf("%s must contain exactly one PEM block", what)
	}
	return block, nil
}
