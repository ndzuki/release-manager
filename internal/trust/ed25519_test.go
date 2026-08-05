package trust

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

type ed25519Resolver struct {
	mu      sync.Mutex
	roots   []*store.TrustRoot
	meta    *store.TrustPolicyMeta
	err     error
	resolve int
}

func (r *ed25519Resolver) ResolveActive(context.Context, string, time.Time) ([]*store.TrustRoot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolve++
	if r.err != nil {
		return nil, r.err
	}
	return r.roots, nil
}

func (r *ed25519Resolver) GetPolicyMeta(context.Context, string) (*store.TrustPolicyMeta, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return r.meta, nil
}

func (r *ed25519Resolver) resolveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resolve
}

func TestEd25519Verifier_VerifiesTrustedSignature(t *testing.T) {
	digest := "sha256:" + fmt.Sprintf("%064x", 1)
	publicKey, privateKey := generateEd25519KeyPair(t)
	resolver := &ed25519Resolver{
		roots: []*store.TrustRoot{{
			ID: "root-1", Environment: "staging", KeyID: "key-1", PublicKeyPEM: publicKey,
			Issuer: "release-manager-ci", SubjectPattern: "repo:release-manager:", State: store.TrustRootActive,
		}},
		meta: &store.TrustPolicyMeta{Environment: "staging", Version: 7, RevocationEpoch: 3},
	}
	verifier := NewEd25519Verifier(newStubStore(), resolver, time.Second, logger())

	out, err := verifier.Verify(t.Context(), Input{
		Digest: digest,
		SignatureRef: &commonv1.SignatureRef{
			Digest: digest, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(digest))),
			Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
		},
		Policy: DefaultPolicy("staging"), Environment: "staging",
	})

	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, out.Status)
	assert.Equal(t, "root-1", out.RootID)
	assert.Equal(t, "key-1", out.KeyID)
	assert.Equal(t, int64(3), out.RevocationEpoch)
	require.NotNil(t, out.Record)
	assert.Equal(t, "7", out.Record.PolicyVersion)
}

func TestEd25519Verifier_ReusesTrustedRecordForSameSignatureIdentity(t *testing.T) {
	digest := "sha256:" + fmt.Sprintf("%064x", 11)
	publicKey, privateKey := generateEd25519KeyPair(t)
	resolver := &ed25519Resolver{
		roots: []*store.TrustRoot{{
			ID: "root-cache", Environment: "staging", KeyID: "key-cache", PublicKeyPEM: publicKey,
			Issuer: "release-manager-ci", State: store.TrustRootActive,
		}},
		meta: &store.TrustPolicyMeta{Environment: "staging", Version: 5, RevocationEpoch: 2},
	}
	verifier := NewEd25519Verifier(newStubStore(), resolver, time.Second, logger())
	input := Input{
		Digest: digest,
		SignatureRef: &commonv1.SignatureRef{
			Digest: digest, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(digest))),
			Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
		},
		Policy: DefaultPolicy("staging"), Environment: "staging",
	}

	first, err := verifier.Verify(t.Context(), input)
	require.NoError(t, err)
	second, err := verifier.Verify(t.Context(), input)
	require.NoError(t, err)

	assert.Equal(t, store.VerificationTrusted, first.Status)
	assert.Equal(t, store.VerificationTrusted, second.Status)
	assert.Equal(t, 1, resolver.resolveCount())
	assert.Equal(t, first.Record.ID, second.Record.ID)
}

func TestEd25519Verifier_DoesNotReuseTrustedRecordForInvalidSignature(t *testing.T) {
	digest := "sha256:" + fmt.Sprintf("%064x", 14)
	publicKey, privateKey := generateEd25519KeyPair(t)
	_, wrongPrivateKey := generateEd25519KeyPair(t)
	resolver := &ed25519Resolver{
		roots: []*store.TrustRoot{{
			ID: "root-cache-signature", Environment: "staging", KeyID: "key-cache-signature", PublicKeyPEM: publicKey,
			Issuer: "release-manager-ci", State: store.TrustRootActive,
		}},
		meta: &store.TrustPolicyMeta{Environment: "staging", Version: 5, RevocationEpoch: 2},
	}
	verifier := NewEd25519Verifier(newStubStore(), resolver, time.Second, logger())
	input := Input{
		Digest: digest,
		SignatureRef: &commonv1.SignatureRef{
			Digest: digest, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(digest))),
			Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
		},
		Policy: DefaultPolicy("staging"), Environment: "staging",
	}

	first, err := verifier.Verify(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, first.Status)

	input.SignatureRef.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(wrongPrivateKey, []byte(digest)))
	second, err := verifier.Verify(t.Context(), input)
	require.NoError(t, err)

	assert.Equal(t, store.VerificationRejected, second.Status)
	assert.Contains(t, second.Summary, "signature_invalid")
	assert.Equal(t, 2, resolver.resolveCount())
}

func TestEd25519Verifier_DoesNotReuseRecordForDifferentSignatureIdentity(t *testing.T) {
	digest := "sha256:" + fmt.Sprintf("%064x", 12)
	publicKey, privateKey := generateEd25519KeyPair(t)
	resolver := &ed25519Resolver{
		roots: []*store.TrustRoot{{
			ID: "root-identity", Environment: "staging", KeyID: "key-identity", PublicKeyPEM: publicKey,
			Issuer: "release-manager-ci", SubjectPattern: "repo:release-manager:", State: store.TrustRootActive,
		}},
		meta: &store.TrustPolicyMeta{Environment: "staging", Version: 6},
	}
	verifier := NewEd25519Verifier(newStubStore(), resolver, time.Second, logger())
	trustedInput := Input{
		Digest: digest,
		SignatureRef: &commonv1.SignatureRef{
			Digest: digest, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(digest))),
			Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
		},
		Policy: DefaultPolicy("staging"), Environment: "staging",
	}
	first, err := verifier.Verify(t.Context(), trustedInput)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, first.Status)

	differentIdentity := trustedInput
	differentIdentity.SignatureRef = &commonv1.SignatureRef{
		Digest: digest, Signature: trustedInput.SignatureRef.GetSignature(),
		Issuer: "release-manager-ci", Subject: "repo:another:ref:refs/heads/main",
	}
	second, err := verifier.Verify(t.Context(), differentIdentity)
	require.NoError(t, err)

	assert.Equal(t, store.VerificationRejected, second.Status)
	assert.Contains(t, second.Summary, "untrusted_issuer")
	assert.Equal(t, 2, resolver.resolveCount())
}

func TestEd25519Verifier_DefaultsMissingPolicyVersionToOne(t *testing.T) {
	digest := "sha256:" + fmt.Sprintf("%064x", 13)
	publicKey, privateKey := generateEd25519KeyPair(t)
	resolver := &ed25519Resolver{
		roots: []*store.TrustRoot{{
			ID: "root-default-version", Environment: "staging", KeyID: "key-default-version", PublicKeyPEM: publicKey,
			Issuer: "release-manager-ci", State: store.TrustRootActive,
		}},
		meta: &store.TrustPolicyMeta{Environment: "staging"},
	}
	verifier := NewEd25519Verifier(newStubStore(), resolver, time.Second, logger())

	out, err := verifier.Verify(t.Context(), Input{
		Digest: digest,
		SignatureRef: &commonv1.SignatureRef{
			Digest: digest, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(digest))),
			Issuer: "release-manager-ci",
		},
		Policy: DefaultPolicy("staging"), Environment: "staging",
	})

	require.NoError(t, err)
	require.NotNil(t, out.Record)
	assert.Equal(t, "1", out.Record.PolicyVersion)
}

func TestEd25519Verifier_RejectsInvalidSignatureAndSubject(t *testing.T) {
	digest := "sha256:" + fmt.Sprintf("%064x", 2)
	publicKey, _ := generateEd25519KeyPair(t)
	_, otherPrivateKey := generateEd25519KeyPair(t)
	resolver := &ed25519Resolver{
		roots: []*store.TrustRoot{{
			ID: "root-1", Environment: "staging", KeyID: "key-1", PublicKeyPEM: publicKey,
			Issuer: "release-manager-ci", SubjectPattern: "repo:release-manager:", State: store.TrustRootActive,
		}},
		meta: &store.TrustPolicyMeta{Environment: "staging", Version: 1},
	}
	tests := []struct {
		name      string
		signature string
		subject   string
		wantCode  string
	}{
		{
			name: "signature from another key", signature: base64.StdEncoding.EncodeToString(ed25519.Sign(otherPrivateKey, []byte(digest))),
			subject: "repo:release-manager:ref:refs/heads/main", wantCode: "signature_invalid",
		},
		{
			name: "subject outside root constraint", signature: base64.StdEncoding.EncodeToString(ed25519.Sign(otherPrivateKey, []byte(digest))),
			subject: "repo:another:ref:refs/heads/main", wantCode: "untrusted_issuer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier := NewEd25519Verifier(newStubStore(), resolver, time.Second, logger())
			out, err := verifier.Verify(t.Context(), Input{
				Digest: digest,
				SignatureRef: &commonv1.SignatureRef{
					Digest: digest, Signature: tt.signature, Issuer: "release-manager-ci", Subject: tt.subject,
				},
				Policy: DefaultPolicy("staging"), Environment: "staging",
			})
			require.NoError(t, err)
			assert.Equal(t, store.VerificationRejected, out.Status)
			assert.Contains(t, out.Summary, tt.wantCode)
		})
	}
}

func TestEd25519Verifier_ReturnsUnavailableWhenResolverFails(t *testing.T) {
	resolver := &ed25519Resolver{err: errors.New("database offline")}
	verifier := NewEd25519Verifier(newStubStore(), resolver, 50*time.Millisecond, logger())

	out, err := verifier.Verify(t.Context(), Input{
		Digest: "sha256:" + fmt.Sprintf("%064x", 3),
		SignatureRef: &commonv1.SignatureRef{
			Digest: "sha256:" + fmt.Sprintf("%064x", 3), Signature: base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)),
			Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
		},
		Policy: DefaultPolicy("production"), Environment: "production",
	})

	require.NoError(t, err)
	assert.Equal(t, store.VerificationVerificationUnavailable, out.Status)
	assert.Contains(t, out.Summary, "verification_unavailable")
}

func TestEd25519Verifier_TimesOutPolicyResolution(t *testing.T) {
	resolver := blockingEd25519Resolver{}
	verifier := NewEd25519Verifier(newStubStore(), resolver, 10*time.Millisecond, logger())
	started := time.Now()

	out, err := verifier.Verify(t.Context(), Input{
		Digest: "sha256:" + fmt.Sprintf("%064x", 15),
		Policy: DefaultPolicy("production"), Environment: "production",
	})

	require.NoError(t, err)
	assert.Equal(t, store.VerificationVerificationUnavailable, out.Status)
	assert.Contains(t, out.Summary, "verification_unavailable")
	assert.Less(t, time.Since(started), time.Second)
}

type blockingEd25519Resolver struct{}

func (blockingEd25519Resolver) ResolveActive(context.Context, string, time.Time) ([]*store.TrustRoot, error) {
	return nil, nil
}

func (blockingEd25519Resolver) GetPolicyMeta(ctx context.Context, _ string) (*store.TrustPolicyMeta, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestEd25519Verifier_InvalidatesCachedTrustedRecordAfterRevocationEpochBump(t *testing.T) {
	st := newStubStore()
	digest := "sha256:" + fmt.Sprintf("%064x", 4)
	ref := &commonv1.SignatureRef{
		Digest: digest, Signature: base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)),
		Issuer: "release-manager-ci", Subject: "repo:release-manager:ref:refs/heads/main",
	}
	require.NoError(t, st.Create(t.Context(), &store.VerificationRecord{
		ArtifactDigest: digest, PolicyVersion: "9", SignatureIdentity: signatureIdentity(ref),
		Status: store.VerificationTrusted, RevocationEpoch: 1,
	}))
	resolver := &ed25519Resolver{
		roots: nil,
		meta:  &store.TrustPolicyMeta{Environment: "production", Version: 9, RevocationEpoch: 2},
	}
	verifier := NewEd25519Verifier(st, resolver, time.Second, logger())

	out, err := verifier.Verify(t.Context(), Input{
		Digest: digest, SignatureRef: ref,
		Policy: DefaultPolicy("production"), Environment: "production",
	})

	require.NoError(t, err)
	assert.Equal(t, store.VerificationRejected, out.Status)
	assert.Contains(t, out.Summary, "untrusted_issuer")
	assert.Equal(t, 1, resolver.resolveCount())
}

func generateEd25519KeyPair(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), privateKey
}
