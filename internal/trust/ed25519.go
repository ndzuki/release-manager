package trust

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

const DefaultVerificationTimeout = 5 * time.Second

// Ed25519Verifier verifies bundle digest signatures against live Trust Roots.
type Ed25519Verifier struct {
	store    store.VerificationStore
	resolver RootResolver
	timeout  time.Duration
	logger   *slog.Logger
}

// NewEd25519Verifier creates the production verifier backed by live trust roots.
func NewEd25519Verifier(st store.VerificationStore, resolver RootResolver, timeout time.Duration, logger *slog.Logger) *Ed25519Verifier {
	if timeout <= 0 {
		timeout = DefaultVerificationTimeout
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Ed25519Verifier{store: st, resolver: resolver, timeout: timeout, logger: logger}
}

// Verify checks a base64-encoded Ed25519 signature over the canonical bundle digest.
func (v *Ed25519Verifier) Verify(ctx context.Context, in Input) (*Output, error) {
	if v.resolver == nil {
		return unavailableOutput("verification_unavailable: live trust root resolver is not configured"), nil
	}

	verifyCtx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	meta, err := v.resolver.GetPolicyMeta(verifyCtx, in.Environment)
	if err != nil || meta == nil {
		v.logResolverFailure("get trust policy metadata", in.Environment, err)
		return unavailableOutput("verification_unavailable: cannot resolve trust policy metadata"), nil
	}
	policyVersion := strconv.FormatInt(meta.Version, 10)
	if meta.Version <= 0 {
		policyVersion = "1"
	}
	if cached := v.cachedRecord(verifyCtx, in, policyVersion, meta.RevocationEpoch); cached != nil {
		return outputFromRecord(cached), nil
	}

	out := v.verifyFresh(verifyCtx, in, meta.RevocationEpoch)
	v.persist(verifyCtx, in, policyVersion, out)
	return out, nil
}

func (v *Ed25519Verifier) cachedRecord(ctx context.Context, in Input, policyVersion string, currentEpoch int64) *store.VerificationRecord {
	if v.store == nil {
		return nil
	}
	record, err := v.store.GetByDigestPolicyAndSignature(ctx, in.Digest, policyVersion, signatureIdentity(in.SignatureRef))
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		v.logger.Warn("verification cache lookup failed", "digest", in.Digest, "policy_version", policyVersion, "error", err)
		return nil
	}
	if currentEpoch > record.RevocationEpoch {
		v.logger.Debug("cached verification invalidated by revocation epoch", "digest", in.Digest, "stored_epoch", record.RevocationEpoch, "current_epoch", currentEpoch)
		return nil
	}
	return record
}

func (v *Ed25519Verifier) verifyFresh(ctx context.Context, in Input, epoch int64) *Output {
	if in.SignatureRef != nil && in.SignatureRef.GetDigest() != "" && in.SignatureRef.GetDigest() != in.Digest {
		return &Output{Status: store.VerificationRejected, Summary: "digest_mismatch: artifact digest does not match signed digest", RevocationEpoch: epoch}
	}
	if in.SignatureRef == nil || in.SignatureRef.GetSignature() == "" {
		return &Output{Status: store.VerificationSignatureMissing, Summary: "signature_missing: artifact has no attached signature", RevocationEpoch: epoch}
	}

	signature, err := base64.StdEncoding.DecodeString(in.SignatureRef.GetSignature())
	if err != nil || len(signature) != ed25519.SignatureSize {
		return &Output{Status: store.VerificationRejected, Summary: "signature_invalid: signature must be a base64 Ed25519 signature", RevocationEpoch: epoch}
	}

	roots, err := v.resolver.ResolveActive(ctx, in.Environment, time.Now().UTC())
	if err != nil {
		v.logResolverFailure("resolve live trust roots", in.Environment, err)
		return unavailableOutputWithEpoch("verification_unavailable: cannot resolve live trust roots", epoch)
	}

	matchedIdentity := false
	for _, root := range roots {
		if !rootMatches(root, in.SignatureRef.GetIssuer(), in.SignatureRef.GetSubject()) {
			continue
		}
		matchedIdentity = true
		key, err := ParsePublicKey(root.PublicKeyPEM)
		if err != nil {
			v.logger.Error("parse live trust root public key failed", "root_id", root.ID, "key_id", root.KeyID, "error", err)
			return unavailableOutputWithEpoch("verification_unavailable: active trust root public key is invalid", epoch)
		}
		publicKey, ok := key.(ed25519.PublicKey)
		if !ok {
			return unavailableOutputWithEpoch("verification_unavailable: active trust root key type is unsupported", epoch)
		}
		if ed25519.Verify(publicKey, []byte(in.Digest), signature) {
			return &Output{
				Status: store.VerificationTrusted, Summary: fmt.Sprintf("trusted: signature matched root %q", root.KeyID),
				RootID: root.ID, KeyID: root.KeyID, RevocationEpoch: epoch,
			}
		}
	}
	if matchedIdentity {
		return &Output{Status: store.VerificationRejected, Summary: "signature_invalid: Ed25519 signature verification failed", RevocationEpoch: epoch}
	}
	return &Output{
		Status:          store.VerificationRejected,
		Summary:         fmt.Sprintf("untrusted_issuer: issuer %q and subject %q do not match live trust roots", in.SignatureRef.GetIssuer(), in.SignatureRef.GetSubject()),
		RevocationEpoch: epoch,
	}
}

func (v *Ed25519Verifier) persist(ctx context.Context, in Input, policyVersion string, out *Output) {
	if v.store == nil || out == nil {
		return
	}
	record := &store.VerificationRecord{
		ID: uuid.NewString(), ArtifactDigest: in.Digest, PolicyVersion: policyVersion,
		SignatureIdentity: signatureIdentity(in.SignatureRef), Status: out.Status,
		RootID: out.RootID, KeyID: out.KeyID, RevocationEpoch: out.RevocationEpoch,
		Issuer: issuerFromRef(in.SignatureRef), Subject: subjectFromRef(in.SignatureRef), Summary: out.Summary,
		CreatedAt: time.Now().UTC(),
	}
	if err := v.store.Create(ctx, record); err != nil {
		v.logger.Warn("failed to persist verification record", "digest", in.Digest, "policy_version", policyVersion, "error", err)
		return
	}
	out.Record = record
}

func signatureIdentity(ref *commonv1.SignatureRef) string {
	if ref == nil {
		return ""
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(ref.GetDigest()))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(ref.GetSignature()))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(ref.GetIssuer()))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(ref.GetSubject()))
	return hex.EncodeToString(hash.Sum(nil))
}

func (v *Ed25519Verifier) logResolverFailure(operation, environment string, err error) {
	if err == nil {
		err = errors.New("empty result")
	}
	v.logger.Error(operation+" failed", "environment", environment, "error", err)
}

func rootMatches(root *store.TrustRoot, issuer, subject string) bool {
	if root == nil {
		return false
	}
	return (&Root{Issuer: root.Issuer, SubjectPattern: root.SubjectPattern}).Matches(issuer, subject)
}

func outputFromRecord(record *store.VerificationRecord) *Output {
	return &Output{
		Status: record.Status, Summary: record.Summary, Record: record, RootID: record.RootID,
		KeyID: record.KeyID, RevocationEpoch: record.RevocationEpoch,
	}
}

func unavailableOutput(summary string) *Output {
	return unavailableOutputWithEpoch(summary, 0)
}

func unavailableOutputWithEpoch(summary string, epoch int64) *Output {
	return &Output{Status: store.VerificationVerificationUnavailable, Summary: summary, RevocationEpoch: epoch}
}

var _ Verifier = (*Ed25519Verifier)(nil)
