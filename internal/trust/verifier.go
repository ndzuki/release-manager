// Package trust implements artifact trust verification for the release pipeline.
package trust

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// ErrVerificationUnavailable is returned when the verification backend is unreachable
// and the policy mandates fail-closed.
var ErrVerificationUnavailable = errors.New("trust: verification backend unavailable")

// Input holds the data needed to verify artifact trust.
type Input struct {
	Digest       string
	SignatureRef *commonv1.SignatureRef
	Policy       store.TrustPolicy
	Environment  string // if set, resolve live roots instead of static policy issuers
}

// Output captures the result of a trust verification.
type Output struct {
	Status          store.VerificationStatus
	Summary         string
	Record          *store.VerificationRecord
	RootID          string
	KeyID           string
	RevocationEpoch int64
}

// Verifier is the interface for artifact trust verification.
type Verifier interface {
	// Verify checks artifact trust and returns the verification outcome.
	// It MUST be idempotent for the same Digest + Policy + signature identity.
	// A different signature_ref for the same digest MUST be verified independently.
	Verify(ctx context.Context, in Input) (*Output, error)
}

// StubVerifier is a test double for policy and cache semantics.
// It does not perform cryptographic verification; production assembly uses
// Ed25519Verifier with a live RootResolver.
type StubVerifier struct {
	st       store.VerificationStore
	resolver RootResolver
	logger   *slog.Logger
}

// NewStubVerifier creates a StubVerifier backed by the given store.
func NewStubVerifier(st store.VerificationStore, r RootResolver, logger *slog.Logger) *StubVerifier {
	return &StubVerifier{st: st, resolver: r, logger: logger}
}

// Verify checks artifact trust against the given policy.
//
// Verification flow (ordered):
//  1. Check store for existing record (AC-012-03: idempotent reuse by policy_version).
//  2. Digest consistency check (AC-012-01: mismatch → rejected).
//  3. Signature presence check (AC-012-02: unsigned → signature_missing).
//  4. Signature format check (valid → trusted; else signature_invalid).
//  5. Issuer trust check (untrusted_issuer).
//  6. Backend unavailable → verification_unavailable + fail closed (AC-012-04).
func (v *StubVerifier) Verify(ctx context.Context, in Input) (*Output, error) {
	// AC-012-03: Idempotent reuse — check store for existing record.
	existing, err := v.st.GetByDigestPolicyAndSignature(ctx, in.Digest, in.Policy.PolicyVersion, signatureIdentity(in.SignatureRef))
	if err == nil {
		// REQ-043 AC-043-04: If a resolver is available, check whether the
		// root that produced this cached record has been revoked since.
		if v.resolver != nil && in.Environment != "" {
			meta, metaErr := v.resolver.GetPolicyMeta(ctx, in.Environment)
			if metaErr == nil && meta.RevocationEpoch > existing.RevocationEpoch {
				v.logger.Debug("cached verification record invalidated by revocation epoch bump",
					"digest", in.Digest,
					"stored_epoch", existing.RevocationEpoch,
					"current_epoch", meta.RevocationEpoch,
				)
				// Re-verify with live roots.
				return v.verifyWithRoots(ctx, in)
			}
		}

		v.logger.Debug("verification record reused",
			"digest", in.Digest,
			"policy_version", in.Policy.PolicyVersion,
			"status", existing.Status,
		)
		return &Output{
			Status:  existing.Status,
			Summary: existing.Summary,
			Record:  existing,
		}, nil
	}
	if err != store.ErrNotFound {
		return nil, fmt.Errorf("verification store lookup: %w", err)
	}

	// Not found — perform fresh verification.
	if v.resolver != nil && in.Environment != "" {
		return v.verifyWithRoots(ctx, in)
	}
	result := v.verify(in)
	return result, nil
}

func (v *StubVerifier) verify(in Input) *Output {
	// AC-012-01: Digest consistency check.
	if in.SignatureRef != nil && in.SignatureRef.Digest != "" {
		if in.SignatureRef.Digest != in.Digest {
			return &Output{
				Status:  store.VerificationRejected,
				Summary: "digest_mismatch: artifact digest does not match signed digest",
			}
		}
	}

	// AC-012-02: Signature presence check.
	if in.SignatureRef == nil || in.SignatureRef.Signature == "" {
		return &Output{
			Status:  store.VerificationSignatureMissing,
			Summary: "signature_missing: artifact has no attached signature",
		}
	}

	// Signature format check.
	if !isValidSignatureFormat(in.SignatureRef.Signature) {
		return &Output{
			Status:  store.VerificationRejected,
			Summary: "signature_invalid: signature format validation failed",
		}
	}

	// Issuer trust check.
	if !isTrustedIssuer(in.SignatureRef.Issuer, in.Policy.TrustedIssuers) {
		return &Output{
			Status:  store.VerificationRejected,
			Summary: fmt.Sprintf("untrusted_issuer: issuer %q is not in trusted issuers list", in.SignatureRef.Issuer),
		}
	}

	return &Output{
		Status:  store.VerificationTrusted,
		Summary: "trusted: digest matches, signature present, issuer trusted",
	}
}

// verifyWithRoots resolves live trust roots and checks issuer against them.
// Active roots pass; grace roots within their window pass; revoked/retired roots fail.
func (v *StubVerifier) verifyWithRoots(ctx context.Context, in Input) (*Output, error) {
	// Digest consistency check.
	if in.SignatureRef != nil && in.SignatureRef.Digest != "" {
		if in.SignatureRef.Digest != in.Digest {
			return &Output{
				Status:  store.VerificationRejected,
				Summary: "digest_mismatch: artifact digest does not match signed digest",
			}, nil
		}
	}

	// Signature presence check.
	if in.SignatureRef == nil || in.SignatureRef.Signature == "" {
		return &Output{
			Status:  store.VerificationSignatureMissing,
			Summary: "signature_missing: artifact has no attached signature",
		}, nil
	}

	// Signature format check.
	if !isValidSignatureFormat(in.SignatureRef.Signature) {
		return &Output{
			Status:  store.VerificationRejected,
			Summary: "signature_invalid: signature format validation failed",
		}, nil
	}

	// Resolve live roots and policy metadata.
	roots, resolveErr := v.resolver.ResolveActive(ctx, in.Environment, time.Now())
	if resolveErr != nil {
		v.logger.Error("live root resolution failed", "env", in.Environment, "err", resolveErr)
		return &Output{
			Status:  store.VerificationRejected,
			Summary: "verification_unavailable: cannot resolve live trust roots",
		}, nil
	}

	meta, metaErr := v.resolver.GetPolicyMeta(ctx, in.Environment)
	epoch := int64(0)
	if metaErr == nil && meta != nil {
		epoch = meta.RevocationEpoch
	}

	// Check if any root accepts this issuer.
	for _, r := range roots {
		if r.Issuer == in.SignatureRef.Issuer {
			return &Output{
				Status:          store.VerificationTrusted,
				Summary:         fmt.Sprintf("trusted: issuer %q matched root %q", in.SignatureRef.Issuer, r.KeyID),
				RootID:          r.ID,
				KeyID:           r.KeyID,
				RevocationEpoch: epoch,
			}, nil
		}
	}

	return &Output{
		Status:  store.VerificationRejected,
		Summary: fmt.Sprintf("untrusted_issuer: issuer %q is not in live trust roots", in.SignatureRef.Issuer),
	}, nil
}

// isValidSignatureFormat performs basic format validation.
// Actual cryptographic verification is deferred to CosignVerifier.
func isValidSignatureFormat(sig string) bool {
	return sig != ""
}

// isTrustedIssuer checks whether the given issuer is in the trusted list.
func isTrustedIssuer(issuer string, trusted []string) bool {
	if len(trusted) == 0 {
		// No trusted issuers configured → reject all (fail-safe).
		return false
	}
	for _, t := range trusted {
		if t == issuer {
			return true
		}
	}
	return false
}

// StoreVerifier is a test-oriented persistence wrapper around another Verifier.
// Production assembly uses Ed25519Verifier, which owns live-policy caching.
type StoreVerifier struct {
	inner  Verifier
	st     store.VerificationStore
	logger *slog.Logger
}

// NewStoreVerifier creates a StoreVerifier that caches results.
func NewStoreVerifier(inner Verifier, st store.VerificationStore, logger *slog.Logger) *StoreVerifier {
	return &StoreVerifier{inner: inner, st: st, logger: logger}
}

// Verify delegates to the inner verifier and persists the result.
func (v *StoreVerifier) Verify(ctx context.Context, in Input) (*Output, error) {
	out, err := v.inner.Verify(ctx, in)
	if err != nil {
		return nil, err
	}

	// Persist the verification result for future idempotent reuse.
	// Use a deterministic ID based on digest + policy_version.
	rec := out.Record
	if rec == nil {
		rec = &store.VerificationRecord{
			ArtifactDigest:    in.Digest,
			PolicyVersion:     in.Policy.PolicyVersion,
			SignatureIdentity: signatureIdentity(in.SignatureRef),
			Status:            out.Status,
			Issuer:            issuerFromRef(in.SignatureRef),
			Subject:           subjectFromRef(in.SignatureRef),
			Summary:           out.Summary,
			RootID:            out.RootID,
			KeyID:             out.KeyID,
			RevocationEpoch:   out.RevocationEpoch,
		}
	}

	if err := v.st.Create(ctx, rec); err != nil {
		v.logger.Warn("failed to persist verification record", "err", err)
		// Non-fatal: verification result is still valid, just not cached.
	}

	out.Record = rec
	return out, nil
}

func issuerFromRef(ref *commonv1.SignatureRef) string {
	if ref == nil {
		return ""
	}
	return ref.Issuer
}

func subjectFromRef(ref *commonv1.SignatureRef) string {
	if ref == nil {
		return ""
	}
	return ref.Subject
}

// StatusToProto converts a store verification status to its proto enum value.
func StatusToProto(s store.VerificationStatus) commonv1.VerificationResult {
	switch s {
	case store.VerificationTrusted:
		return commonv1.VerificationResult_VERIFICATION_RESULT_TRUSTED
	case store.VerificationRejected:
		return commonv1.VerificationResult_VERIFICATION_RESULT_REJECTED
	case store.VerificationPolicyWarning:
		return commonv1.VerificationResult_VERIFICATION_RESULT_POLICY_WARNING
	case store.VerificationSignatureMissing:
		return commonv1.VerificationResult_VERIFICATION_RESULT_SIGNATURE_MISSING
	case store.VerificationVerificationUnavailable:
		return commonv1.VerificationResult_VERIFICATION_RESULT_VERIFICATION_UNAVAILABLE
	default:
		return commonv1.VerificationResult_VERIFICATION_RESULT_UNSPECIFIED
	}
}

// Compile-time check: StubVerifier implements Verifier.
var _ Verifier = (*StubVerifier)(nil)
var _ Verifier = (*StoreVerifier)(nil)
