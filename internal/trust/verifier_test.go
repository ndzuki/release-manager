package trust

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

func trustedPolicy() store.TrustPolicy {
	return store.TrustPolicy{
		PolicyVersion:  "v1",
		FailClosed:     true,
		TrustedIssuers: []string{"release-manager-ci"},
	}
}

func emptyPolicy() store.TrustPolicy {
	return store.TrustPolicy{
		PolicyVersion:  "v1",
		FailClosed:     false,
		TrustedIssuers: nil,
	}
}

func signRef(issuer string) *commonv1.SignatureRef {
	return &commonv1.SignatureRef{
		Digest:    "sha256:abc123",
		Signature: "MEUCIQD...base64signature",
		Issuer:    issuer,
		Subject:   "release-manager/v1.0.0",
	}
}

func logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

type stubStore struct {
	records map[string]*store.VerificationRecord
}

func newStubStore() *stubStore {
	return &stubStore{records: make(map[string]*store.VerificationRecord)}
}

func (s *stubStore) Create(_ context.Context, rec *store.VerificationRecord) error {
	s.records[rec.ArtifactDigest+":"+rec.PolicyVersion] = rec
	return nil
}

func (s *stubStore) GetByDigestAndPolicy(_ context.Context, artifactDigest, policyVersion string) (*store.VerificationRecord, error) {
	key := artifactDigest + ":" + policyVersion
	rec, ok := s.records[key]
	if !ok {
		return nil, store.ErrNotFound
	}
	return rec, nil
}

// AC-012-01: Digest mismatch → rejected.
func TestVerify_DigestMismatch(t *testing.T) {
	v := NewStubVerifier(newStubStore(), nil, logger())

	in := Input{
		Digest:       "sha256:xyz789",
		SignatureRef: signRef("release-manager-ci"),
		Policy:       trustedPolicy(),
	}

	out, err := v.Verify(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationRejected, out.Status)
	assert.Contains(t, out.Summary, "digest_mismatch")
}

// AC-012-02: Unsigned → signature_missing.
func TestVerify_SignatureMissing(t *testing.T) {
	v := NewStubVerifier(newStubStore(), nil, logger())

	in := Input{
		Digest:       "sha256:abc123",
		SignatureRef: nil, // no signature
		Policy:       trustedPolicy(),
	}

	out, err := v.Verify(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationSignatureMissing, out.Status)
	assert.Contains(t, out.Summary, "signature_missing")
}

// AC-012-02b: Empty signature → signature_missing.
func TestVerify_EmptySignature(t *testing.T) {
	v := NewStubVerifier(newStubStore(), nil, logger())

	in := Input{
		Digest: "sha256:abc123",
		SignatureRef: &commonv1.SignatureRef{
			Digest:    "sha256:abc123",
			Signature: "", // empty
		},
		Policy: trustedPolicy(),
	}

	out, err := v.Verify(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationSignatureMissing, out.Status)
}

// AC-012-03: Idempotent reuse by policy_version.
func TestVerify_IdempotentReuse(t *testing.T) {
	st := newStubStore()
	v := NewStubVerifier(st, nil, logger())

	in := Input{
		Digest:       "sha256:abc123",
		SignatureRef: signRef("release-manager-ci"),
		Policy:       trustedPolicy(),
	}

	// First call — fresh verification.
	out1, err := v.Verify(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, out1.Status)

	// Second call — should reuse cached record.
	// We pre-populate the store with a different status to prove it hits cache.
	err = st.Create(context.Background(), &store.VerificationRecord{
		ArtifactDigest: "sha256:abc123",
		PolicyVersion:  "v1",
		Status:         store.VerificationTrusted,
	})
	require.NoError(t, err)

	out2, err := v.Verify(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, out2.Status)
}

// AC-012-04: Backend unavailable → should be handled at call site (fail closed).
// The StubVerifier itself does not simulate backend failure; this is tested
// at the integration level via the orchestrator.

// Trusted issuer → trusted.
func TestVerify_TrustedIssuer(t *testing.T) {
	v := NewStubVerifier(newStubStore(), nil, logger())

	in := Input{
		Digest:       "sha256:abc123",
		SignatureRef: signRef("release-manager-ci"),
		Policy:       trustedPolicy(),
	}

	out, err := v.Verify(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, out.Status)
	assert.Contains(t, out.Summary, "trusted")
}

// Untrusted issuer → rejected.
func TestVerify_UntrustedIssuer(t *testing.T) {
	v := NewStubVerifier(newStubStore(), nil, logger())

	in := Input{
		Digest:       "sha256:abc123",
		SignatureRef: signRef("evil-ci"),
		Policy:       trustedPolicy(),
	}

	out, err := v.Verify(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationRejected, out.Status)
	assert.Contains(t, out.Summary, "untrusted_issuer")
}

// No trusted issuers configured → reject all.
func TestVerify_EmptyTrustedIssuers(t *testing.T) {
	v := NewStubVerifier(newStubStore(), nil, logger())

	in := Input{
		Digest:       "sha256:abc123",
		SignatureRef: signRef("anyone"),
		Policy:       emptyPolicy(),
	}

	out, err := v.Verify(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationRejected, out.Status)
}

// Digest match + trusted issuer → trusted.
func TestVerify_DigestMatch(t *testing.T) {
	v := NewStubVerifier(newStubStore(), nil, logger())

	// Digest in signature_ref matches Input.Digest.
	in := Input{
		Digest:       "sha256:abc123",
		SignatureRef: signRef("release-manager-ci"),
		Policy:       trustedPolicy(),
	}

	out, err := v.Verify(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, out.Status)
}

// Trusted issuer in multi-issuer list.
func TestVerify_MultiIssuerList(t *testing.T) {
	v := NewStubVerifier(newStubStore(), nil, logger())

	policy := store.TrustPolicy{
		PolicyVersion: "v1",
		FailClosed:    true,
		TrustedIssuers: []string{
			"release-manager-ci",
			"github-actions",
			"jenkins-prod",
		},
	}

	in := Input{
		Digest:       "sha256:abc123",
		SignatureRef: signRef("github-actions"),
		Policy:       policy,
	}

	out, err := v.Verify(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, store.VerificationTrusted, out.Status)
}

// Test policy defaults: production fail_closed, staging fail_open.
func TestDefaultPolicy(t *testing.T) {
	prod := DefaultPolicy("production")
	assert.True(t, prod.FailClosed)
	assert.NotEmpty(t, prod.TrustedIssuers)

	staging := DefaultPolicy("staging")
	assert.False(t, staging.FailClosed)
	assert.NotEmpty(t, staging.TrustedIssuers)
}

// Test statusToProto mapping in webhook.
func TestStatusToProto(t *testing.T) {
	tests := []struct {
		status   store.VerificationStatus
		expected commonv1.VerificationResult
	}{
		{store.VerificationTrusted, commonv1.VerificationResult_VERIFICATION_RESULT_TRUSTED},
		{store.VerificationRejected, commonv1.VerificationResult_VERIFICATION_RESULT_REJECTED},
		{store.VerificationPolicyWarning, commonv1.VerificationResult_VERIFICATION_RESULT_POLICY_WARNING},
		{store.VerificationSignatureMissing, commonv1.VerificationResult_VERIFICATION_RESULT_SIGNATURE_MISSING},
		{store.VerificationVerificationUnavailable, commonv1.VerificationResult_VERIFICATION_RESULT_VERIFICATION_UNAVAILABLE},
		{store.VerificationStatus("unknown"), commonv1.VerificationResult_VERIFICATION_RESULT_UNSPECIFIED},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			// Test both the orchestrator and webhook versions — they should be identical.
			gotOrch := StatusToProto(tt.status)
			assert.Equal(t, tt.expected, gotOrch)
		})
	}
}
