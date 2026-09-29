package orchestrator

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-220: the only production writer of verification records is CreateOperation, and it
// signs the BUNDLE digest -- a bundle is what CI signs, while a candidate artifact is an
// image inside it and common.v1.BundleImage carries no signature of its own. These tests
// drive that production shape (a trusted record under the bundle digest plus the link that
// delivered the image) instead of the artifact-keyed record the older fixtures wrote by
// hand, which is why the mismatch survived a green suite.
func seedArtifactTrustedThroughBundle(
	t *testing.T, st store.Store, artifactID, digest string, status store.VerificationStatus, link bool,
) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
		ID: artifactID, ArtifactType: store.ArtifactImage,
		Ref: "registry.example.com/team/api@" + digest, Digest: digest,
		CreatedAt: now, LastSeenAt: now, ValidatedAt: &now,
	}))
	// The bundle digest MUST differ from the image digest: that difference is the whole
	// point of TASK-220. Deriving it from the image digest (the first version of this
	// helper did) made the artifact-keyed lookup succeed and the test proved nothing.
	bundle := &store.ReleaseBundle{
		ID: "bundle-" + artifactID, Name: "bundle-" + artifactID, DigestAlg: "sha256",
		DigestValue: "bundle-digest-" + strings.TrimPrefix(digest, "sha256:"),
		Status:      store.BundleValidated, CreatedAt: now,
	}
	require.NoError(t, st.Bundles().Create(t.Context(), bundle))
	// A trusted verdict names the root that signed it, as the shipping verifier records: the
	// emergency gate refuses a verdict it cannot attribute to a live root (TASK-225).
	root := seedEmergencyLiveRoot(t, st)
	require.NoError(t, st.Verifications().Create(t.Context(), &store.VerificationRecord{
		ID: uuid.NewString(), ArtifactDigest: bundle.DigestAlg + ":" + bundle.DigestValue,
		PolicyVersion: emergencyTestPolicyVersion(t, st), Status: status, RevocationEpoch: 0, CreatedAt: now,
		RootID: root.ID, KeyID: root.KeyID,
	}))
	if link {
		require.NoError(t, st.CandidateArtifacts().LinkToBundle(t.Context(), artifactID, bundle.ID))
	}
}

func emergencyImageRequestForArtifact(key, artifactID string) *connect.Request[orchestratorv1.ExecuteEmergencyChangeRequest] {
	req := emergencyImageRequest(key)
	req.Msg.ArtifactRef = artifactID
	return req
}

func TestEmergencyArtifactTrustResolvesThroughItsBundle(t *testing.T) {
	t.Run("a trusted bundle authorises the image it delivered", func(t *testing.T) {
		svc, st, dispatcher := emergencyTestService(t)
		seedArtifactTrustedThroughBundle(t, st, "artifact-via-trusted", "sha256:via-trusted", store.VerificationTrusted, true)

		resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequestForArtifact("via-trusted", "artifact-via-trusted"))
		require.NoError(t, err)
		assert.True(t, resp.Msg.GetResult().GetRequested())
		require.Len(t, dispatcher.commands, 1)
	})

	t.Run("an untrusted bundle does not", func(t *testing.T) {
		svc, st, dispatcher := emergencyTestService(t)
		seedArtifactTrustedThroughBundle(t, st, "artifact-via-untrusted", "sha256:via-untrusted", store.VerificationSignatureMissing, true)

		_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequestForArtifact("via-untrusted", "artifact-via-untrusted"))
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		assert.Equal(t, "artifact_not_trusted", connectErrorReason(err))
		assert.Empty(t, dispatcher.commands)
	})

	t.Run("an artifact no trusted bundle delivered is refused", func(t *testing.T) {
		svc, st, dispatcher := emergencyTestService(t)
		// The bundle is trusted, but it did not deliver this artifact: nothing links them.
		seedArtifactTrustedThroughBundle(t, st, "artifact-unlinked", "sha256:unlinked", store.VerificationTrusted, false)

		_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequestForArtifact("unlinked", "artifact-unlinked"))
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		assert.Equal(t, "artifact_not_trusted", connectErrorReason(err))
		assert.Empty(t, dispatcher.commands)
	})

	t.Run("an unsigned operation running later does not shadow a trusted verdict", func(t *testing.T) {
		svc, st, dispatcher := emergencyTestService(t)
		seedArtifactTrustedThroughBundle(t, st, "artifact-shadow", "sha256:shadow", store.VerificationTrusted, true)
		// A later operation that carried NO signature records `signature_missing` for the
		// same bundle digest. That row states no verdict about the artifact, so it must not
		// downgrade the trusted one the signed operation wrote. Observed in dev: four rows
		// for one digest, three `signature_missing` and one `trusted`.
		require.NoError(t, st.Verifications().Create(t.Context(), &store.VerificationRecord{
			ID: uuid.NewString(), ArtifactDigest: "sha256:bundle-digest-shadow", PolicyVersion: emergencyTestPolicyVersion(t, st),
			Status: store.VerificationSignatureMissing, CreatedAt: time.Now().UTC().Add(time.Minute),
		}))

		resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequestForArtifact("shadow", "artifact-shadow"))
		require.NoError(t, err)
		assert.True(t, resp.Msg.GetResult().GetRequested())
		require.Len(t, dispatcher.commands, 1)
	})

	t.Run("a later rejection still wins", func(t *testing.T) {
		svc, st, dispatcher := emergencyTestService(t)
		seedArtifactTrustedThroughBundle(t, st, "artifact-rejected-later", "sha256:rejected-later", store.VerificationTrusted, true)
		// A real re-verification that rejected the artifact outranks the earlier trust
		// decision: the gate follows the LATEST verdict, not the friendliest one.
		require.NoError(t, st.Verifications().Create(t.Context(), &store.VerificationRecord{
			ID: uuid.NewString(), ArtifactDigest: "sha256:bundle-digest-rejected-later", PolicyVersion: emergencyTestPolicyVersion(t, st),
			Status: store.VerificationRejected, CreatedAt: time.Now().UTC().Add(time.Minute),
		}))

		_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequestForArtifact("rejected-later", "artifact-rejected-later"))
		require.Error(t, err)
		assert.Equal(t, "artifact_not_trusted", connectErrorReason(err))
		assert.Empty(t, dispatcher.commands)
	})

	t.Run("a verdict on the artifact itself is final", func(t *testing.T) {
		svc, st, dispatcher := emergencyTestService(t)
		// The image was rejected under its own digest while an unrelated-but-trusted bundle
		// happens to contain it. The artifact's own verdict must win: falling back to the
		// bundle here would let a rejection be overridden silently (found by review).
		seedArtifactTrustedThroughBundle(t, st, "artifact-own-rejected", "sha256:own-rejected", store.VerificationTrusted, true)
		require.NoError(t, st.Verifications().Create(t.Context(), &store.VerificationRecord{
			ID: uuid.NewString(), ArtifactDigest: "sha256:own-rejected", PolicyVersion: emergencyTestPolicyVersion(t, st),
			Status: store.VerificationRejected, CreatedAt: time.Now().UTC().Add(time.Minute),
		}))

		_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequestForArtifact("own-rejected", "artifact-own-rejected"))
		require.Error(t, err)
		assert.Equal(t, "artifact_not_trusted", connectErrorReason(err))
		assert.Empty(t, dispatcher.commands)
	})

	t.Run("a verdict on the artifact alone is enough", func(t *testing.T) {
		svc, st, dispatcher := emergencyTestService(t)
		now := time.Now().UTC()
		require.NoError(t, st.CandidateArtifacts().Create(t.Context(), &store.CandidateArtifact{
			ID: "artifact-own-trusted", ArtifactType: store.ArtifactImage,
			Ref: "registry.example.com/team/api@sha256:own-trusted", Digest: "sha256:own-trusted",
			CreatedAt: now, LastSeenAt: now, ValidatedAt: &now,
		}))
		root := seedEmergencyLiveRoot(t, st)
		require.NoError(t, st.Verifications().Create(t.Context(), &store.VerificationRecord{
			ID: uuid.NewString(), ArtifactDigest: "sha256:own-trusted", PolicyVersion: emergencyTestPolicyVersion(t, st),
			Status: store.VerificationTrusted, CreatedAt: now, RootID: root.ID, KeyID: root.KeyID,
		}))

		resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequestForArtifact("own-trusted", "artifact-own-trusted"))
		require.NoError(t, err)
		assert.True(t, resp.Msg.GetResult().GetRequested())
		require.Len(t, dispatcher.commands, 1)
	})

	t.Run("a stale revocation epoch still refuses", func(t *testing.T) {
		svc, st, dispatcher := emergencyTestService(t)
		seedArtifactTrustedThroughBundle(t, st, "artifact-revoked", "sha256:revoked", store.VerificationTrusted, true)
		// Revoking a root bumps the environment's revocation epoch; a record written before
		// that no longer authorises anything (AC-220-02). trust/service.go performs the
		// production revoke through TransitionLiveRoot(..., bumpRevocation=true). The root
		// being revoked is a keeper, not the root that signed the verdict: the verdict's
		// root stays live, so the only reason left for the refusal is the stale epoch.
		now := time.Now().UTC()
		require.NoError(t, st.TrustRoots().Create(t.Context(), &store.TrustRoot{
			ID: "root-epoch-keeper", Environment: "staging", KeyID: "root-epoch-keeper-key",
			Issuer: "release-manager-ci", SubjectPattern: "*", State: store.TrustRootActive,
			ValidFrom: now.Add(-time.Hour),
		}))
		_, err := st.TrustRoots().TransitionLiveRoot(
			t.Context(), "root-epoch-keeper", "staging", store.TrustRootRevoked, &now, true)
		require.NoError(t, err)

		_, err = svc.ExecuteEmergencyChange(emergencyAdminContext(), emergencyImageRequestForArtifact("revoked", "artifact-revoked"))
		require.Error(t, err)
		assert.Equal(t, "artifact_not_trusted", connectErrorReason(err))
		assert.Empty(t, dispatcher.commands)
	})
}
