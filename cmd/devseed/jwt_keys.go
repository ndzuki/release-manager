package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ndzuki/release-manager/internal/jwtauth"
)

// The dev management-plane JWT key pair (REQ-065 AC-065-01 / decision D1=A):
// access tokens are EdDSA (Ed25519), so the dev environment needs a real key
// pair rather than the symmetric secret it used to mint. The two halves are
// written as separate PEM files because they go to different Secrets — the
// private half is mounted only into cmd/auth, the public half only into
// cmd/orchestrator (see deploy/kustomize/dev/kustomization.yaml).
const (
	jwtPrivateKeyFileName = "jwt-private-key.pem"
	jwtPublicKeyFileName  = "jwt-public-key.pem"
)

// ensureDevJWTKeys owns the whole dev key contract, mirroring ensureDevMTLSCA:
//
//   - an explicit private key (DEV_JWT_PRIVATE_KEY, e.g. the ci profile's
//     Secret) wins: it is validated through the same parser cmd/auth uses and
//     the public half is DERIVED from it, so the two files can never disagree;
//   - otherwise an existing parseable pair is reused (rotation = delete the
//     directory and re-run dev-up);
//   - otherwise a fresh Ed25519 pair is generated.
//
// It returns the private and public file paths.
func ensureDevJWTKeys(dir, privateKeyInput string) (privatePath, publicPath string, err error) {
	privatePath = filepath.Join(dir, jwtPrivateKeyFileName)
	publicPath = filepath.Join(dir, jwtPublicKeyFileName)

	var privateKey ed25519.PrivateKey
	if privateKeyInput != "" {
		privateKey, err = jwtauth.ParseEd25519PrivateKeyPEM(privateKeyInput)
		if err != nil {
			return "", "", fmt.Errorf("DEV_JWT_PRIVATE_KEY: %w", err)
		}
		if err := writeDevJWTKeys(dir, privatePath, publicPath, privateKey); err != nil {
			return "", "", err
		}
		fmt.Printf("  jwt key pair (from DEV_JWT_PRIVATE_KEY) .. %s\n", dir)
		return privatePath, publicPath, nil
	}

	reusable, reason := devJWTKeysReusable(privatePath, publicPath)
	if reusable {
		fmt.Printf("  jwt key pair (reused) ............. %s\n", dir)
		return privatePath, publicPath, nil
	}
	// A half-written or mismatched pair is regenerated rather than served: the
	// dev environment must never run with a private key whose public half does
	// not match what the verifiers were given. A completely absent pair is the
	// normal first-run case, so it is not reported as a repair.
	if _, statErr := os.Stat(privatePath); statErr == nil {
		fmt.Printf("  jwt key pair (regenerating: %v)\n", reason)
	} else if _, statErr := os.Stat(publicPath); statErr == nil {
		fmt.Printf("  jwt key pair (regenerating: %v)\n", reason)
	}

	_, privateKey, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate jwt key: %w", err)
	}
	if err := writeDevJWTKeys(dir, privatePath, publicPath, privateKey); err != nil {
		return "", "", err
	}
	fmt.Printf("  jwt key pair (generated) .......... %s\n", dir)
	return privatePath, publicPath, nil
}

// devJWTKeysReusable reports whether both files exist, parse through the exact
// parsers the services use, and actually form a pair. A nil reason means the
// pair is usable; a non-nil reason explains why it is not.
func devJWTKeysReusable(privatePath, publicPath string) (bool, error) {
	privatePEM, privateErr := os.ReadFile(privatePath)
	publicPEM, publicErr := os.ReadFile(publicPath)
	if privateErr != nil || publicErr != nil {
		return false, fmt.Errorf("key pair incomplete (private=%v public=%v)", privateErr, publicErr)
	}
	privateKey, err := jwtauth.ParseEd25519PrivateKeyPEM(string(privatePEM))
	if err != nil {
		return false, fmt.Errorf("private key unparseable: %w", err)
	}
	publicKey, err := jwtauth.ParseEd25519PublicKeyPEM(string(publicPEM))
	if err != nil {
		return false, fmt.Errorf("public key unparseable: %w", err)
	}
	if !publicKey.Equal(privateKey.Public()) {
		return false, errors.New("public key does not match the private key")
	}
	return true, nil
}

// writeDevJWTKeys persists the pair atomically. The private half is 0600; the
// public half is 0644 because it is not secret (and the directory itself is
// 0700, so neither is world-readable in practice).
func writeDevJWTKeys(dir, privatePath, publicPath string, privateKey ed25519.PrivateKey) error {
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("marshal jwt private key: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		return fmt.Errorf("marshal jwt public key: %w", err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})

	// Validate through the exact parsers the services use, so a format change
	// fails here instead of at service boot.
	if _, err := jwtauth.ParseEd25519PrivateKeyPEM(string(privatePEM)); err != nil {
		return fmt.Errorf("generated jwt private key fails the service parser: %w", err)
	}
	if _, err := jwtauth.ParseEd25519PublicKeyPEM(string(publicPEM)); err != nil {
		return fmt.Errorf("generated jwt public key fails the service parser: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create jwt key dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod jwt key dir: %w", err)
	}
	if err := writeFileAtomic(privatePath, privatePEM, 0o600); err != nil {
		return fmt.Errorf("write jwt private key: %w", err)
	}
	if err := writeFileAtomic(publicPath, publicPEM, 0o644); err != nil {
		return fmt.Errorf("write jwt public key: %w", err)
	}
	return nil
}
