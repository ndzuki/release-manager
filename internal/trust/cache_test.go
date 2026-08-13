package trust

import (
	"crypto/ed25519"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	trustv1 "github.com/ndzuki/release-manager/api/gen/trust/v1"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestEd25519Verifier_RevocationEpochReverifiesAgainstRemainingRoot(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	oldPublicKeyPEM, oldPrivateKey := generateEd25519KeyPair(t)
	newPublicKeyPEM, _ := generateEd25519KeyPair(t)
	service := NewTrustService(st.TrustRoots(), nil, logger())

	oldResponse, err := service.CreateTrustRoot(t.Context(), connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "staging", KeyId: "key-old", PublicKeyPem: oldPublicKeyPEM, Issuer: "old-ci",
	}))
	require.NoError(t, err)
	_, err = service.CreateTrustRoot(t.Context(), connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "staging", KeyId: "key-new", PublicKeyPem: newPublicKeyPEM, Issuer: "new-ci",
	}))
	require.NoError(t, err)

	digest := "sha256:cache-revoke"
	ref := &commonv1.SignatureRef{
		Digest:    digest,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(oldPrivateKey, []byte(digest))),
		Issuer:    "old-ci", Subject: "release-manager/v1.0.0",
	}
	input := Input{Digest: digest, SignatureRef: ref, Policy: DefaultPolicy("staging"), Environment: "staging"}
	verifier := NewEd25519Verifier(st.Verifications(), NewStoreResolver(st.TrustRoots()), time.Second, logger())

	first, err := verifier.Verify(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, first.Status)

	_, err = service.RevokeTrustRoot(t.Context(), connect.NewRequest(&trustv1.RevokeTrustRootRequest{
		Environment: "staging", RootId: oldResponse.Msg.GetRoot().GetId(),
	}))
	require.NoError(t, err)

	second, err := verifier.Verify(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationRejected, second.Status)
	assert.Contains(t, second.Summary, "untrusted_issuer")
}

func TestEd25519Verifier_ConcurrentVerifyAndRevokeIsRaceSafe(t *testing.T) {
	st := sqlitestore.OpenTest(t)
	oldPublicKeyPEM, oldPrivateKey := generateEd25519KeyPair(t)
	newPublicKeyPEM, _ := generateEd25519KeyPair(t)
	service := NewTrustService(st.TrustRoots(), nil, logger())

	oldResponse, err := service.CreateTrustRoot(t.Context(), connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "staging", KeyId: "key-old", PublicKeyPem: oldPublicKeyPEM, Issuer: "old-ci",
	}))
	require.NoError(t, err)
	_, err = service.CreateTrustRoot(t.Context(), connect.NewRequest(&trustv1.CreateTrustRootRequest{
		Environment: "staging", KeyId: "key-new", PublicKeyPem: newPublicKeyPEM, Issuer: "new-ci",
	}))
	require.NoError(t, err)

	digest := "sha256:cache-concurrent"
	ref := &commonv1.SignatureRef{
		Digest:    digest,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(oldPrivateKey, []byte(digest))),
		Issuer:    "old-ci", Subject: "release-manager/v1.0.0",
	}
	input := Input{Digest: digest, SignatureRef: ref, Policy: DefaultPolicy("staging"), Environment: "staging"}
	verifier := NewEd25519Verifier(st.Verifications(), NewStoreResolver(st.TrustRoots()), time.Second, logger())
	first, err := verifier.Verify(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, first.Status)

	const readers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, readers+1)
	outputs := make(chan store.VerificationStatus, readers)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, revokeErr := service.RevokeTrustRoot(t.Context(), connect.NewRequest(&trustv1.RevokeTrustRootRequest{
			Environment: "staging", RootId: oldResponse.Msg.GetRoot().GetId(),
		}))
		errs <- revokeErr
	}()
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, verifyErr := verifier.Verify(t.Context(), input)
			if verifyErr != nil {
				errs <- verifyErr
				return
			}
			outputs <- out.Status
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(outputs)
	for err := range errs {
		require.NoError(t, err)
	}
	for status := range outputs {
		assert.Contains(t, []store.VerificationStatus{store.VerificationTrusted, store.VerificationRejected}, status)
	}

	final, err := verifier.Verify(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationRejected, final.Status)
}

func TestStubVerifier_RevocationEpochInvalidatesTrustedCache(t *testing.T) {
	const digest = "sha256:abc123"
	ref := &commonv1.SignatureRef{
		Digest: digest, Signature: "cached-signature", Issuer: "release-manager-ci", Subject: "release-manager/v1.0.0",
	}
	st := newStubStore()
	require.NoError(t, st.Create(t.Context(), &store.VerificationRecord{
		ArtifactDigest: digest, PolicyVersion: "v1", SignatureIdentity: signatureIdentity(ref),
		Status: store.VerificationTrusted, RevocationEpoch: 1, Summary: "cached trusted result",
	}))
	resolver := &ed25519Resolver{meta: &store.TrustPolicyMeta{Environment: "production", Version: 1, RevocationEpoch: 2}}
	v := NewStubVerifier(st, resolver, logger())
	out, err := v.Verify(t.Context(), Input{Digest: digest, SignatureRef: ref, Policy: trustedPolicy(), Environment: "production"})
	require.NoError(t, err)
	assert.Equal(t, store.VerificationRejected, out.Status)
	assert.Contains(t, out.Summary, "untrusted_issuer")
}

func TestStubVerifier_ReusesCacheWhenRevocationEpochUnchanged(t *testing.T) {
	const digest = "sha256:abc123"
	ref := &commonv1.SignatureRef{
		Digest: digest, Signature: "cached-signature", Issuer: "release-manager-ci", Subject: "release-manager/v1.0.0",
	}
	st := newStubStore()
	require.NoError(t, st.Create(t.Context(), &store.VerificationRecord{
		ArtifactDigest: digest, PolicyVersion: "v1", SignatureIdentity: signatureIdentity(ref),
		Status: store.VerificationTrusted, RevocationEpoch: 2, Summary: "cached trusted result",
	}))
	resolver := &ed25519Resolver{meta: &store.TrustPolicyMeta{Environment: "production", Version: 1, RevocationEpoch: 2}}
	v := NewStubVerifier(st, resolver, logger())
	out, err := v.Verify(t.Context(), Input{Digest: digest, SignatureRef: ref, Policy: trustedPolicy(), Environment: "production"})
	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, out.Status)
	assert.Equal(t, "cached trusted result", out.Summary)
}
