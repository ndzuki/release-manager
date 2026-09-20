package orchestrator

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// sha256Digest builds a well-formed digest for the fixtures.
func sha256Digest(t *testing.T, seed string) string {
	t.Helper()
	// 64 lowercase hex characters; the value itself is irrelevant to identity.
	return "sha256:" + strings.Repeat("0", 64-len(seed)) + seed
}

func validSubmitRequest(t *testing.T) *orchestratorv1.SubmitBundleRequest {
	t.Helper()
	return &orchestratorv1.SubmitBundleRequest{
		Name:         "bundle-1",
		ChartRef:     "oci://reg/charts/app",
		ChartVersion: "1.2.3",
		ChartDigest:  sha256Digest(t, "a"),
		GitCommit:    "abc123",
		PipelineId:   "pipe-1",
		Images: []*commonv1.BundleImage{{
			Ref: "reg/app:v1", Digest: sha256Digest(t, "b"),
			ValuesPath: "image.repository", ValueKind: commonv1.ImageValueKind_IMAGE_VALUE_KIND_DIGEST,
		}},
	}
}

// AC-011-01: every image is bound to its values path, so a multi-image bundle
// keeps one binding per image.
func TestBundleFromProtoBindsEveryImageToItsValuesPath(t *testing.T) {
	msg := validSubmitRequest(t)
	msg.Images = []*commonv1.BundleImage{
		{Ref: "reg/app:v1", Digest: sha256Digest(t, "b"), ValuesPath: "api.image", ValueKind: commonv1.ImageValueKind_IMAGE_VALUE_KIND_DIGEST},
		{Ref: "reg/sidecar:v1", Digest: sha256Digest(t, "c"), ValuesPath: "sidecar.image", ValueKind: commonv1.ImageValueKind_IMAGE_VALUE_KIND_TAG},
	}

	bundle, candidates := bundleFromProto(msg)

	require.Len(t, bundle.Images, 2)
	assert.Equal(t, "api.image", bundle.Images[0].ValuesPath)
	assert.Equal(t, store.ImageValueDigest, bundle.Images[0].ValueKind)
	assert.Equal(t, "sidecar.image", bundle.Images[1].ValuesPath)
	assert.Equal(t, store.ImageValueTag, bundle.Images[1].ValueKind)

	// Both images also become candidates, each keyed by its own digest.
	digests := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.ArtifactType == store.ArtifactImage {
			digests[candidate.Digest] = true
		}
	}
	assert.True(t, digests[sha256Digest(t, "b")] && digests[sha256Digest(t, "c")],
		"AC-011-01: both images must appear as candidates")
}

// AC-011-03: a missing or malformed digest is rejected before anything is written.
func TestValidateBundleIdentityRejectsMissingAndMalformedDigests(t *testing.T) {
	t.Run("missing chart digest", func(t *testing.T) {
		msg := validSubmitRequest(t)
		msg.ChartDigest = ""
		err := validateBundleIdentity(msg)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		// bundleError carries the stable code as the message prefix rather than in
		// Connect metadata (unlike the values-approval and emergency helpers).
		assert.Contains(t, err.Error(), "missing_chart_digest",
			"expected the missing_chart_digest code, got %q", err.Error())
	})

	t.Run("malformed chart digest", func(t *testing.T) {
		msg := validSubmitRequest(t)
		msg.ChartDigest = "sha256:NOT-HEX"
		err := validateBundleIdentity(msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid_digest_format",
			"expected the invalid_digest_format code, got %q", err.Error())
	})

	t.Run("uppercase hex is not canonical", func(t *testing.T) {
		msg := validSubmitRequest(t)
		msg.ChartDigest = "sha256:" + strings.ToUpper(strings.Repeat("a", 64))
		err := validateBundleIdentity(msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid_digest_format",
			"expected the invalid_digest_format code, got %q", err.Error())
	})

	t.Run("well-formed digest passes", func(t *testing.T) {
		assert.NoError(t, validateBundleIdentity(validSubmitRequest(t)))
	})
}

// AC-011-05: candidates are derived from the chart, every image, and each
// evidence digest, not just the images.
func TestDeriveCandidatesCoversEveryDigestSource(t *testing.T) {
	msg := validSubmitRequest(t)
	msg.Signature = &commonv1.ArtifactReference{Ref: "reg/sig", Digest: sha256Digest(t, "d")}
	msg.Sbom = &commonv1.ArtifactReference{Ref: "reg/sbom", Digest: sha256Digest(t, "e")}
	msg.Provenance = &commonv1.ArtifactReference{Ref: "reg/prov", Digest: sha256Digest(t, "f")}

	byType := map[store.ArtifactType]string{}
	for _, candidate := range deriveCandidates(msg) {
		byType[candidate.ArtifactType] = candidate.Digest
	}

	assert.Equal(t, sha256Digest(t, "a"), byType[store.ArtifactChart], "AC-011-05: the chart digest is a candidate")
	assert.Equal(t, sha256Digest(t, "b"), byType[store.ArtifactImage])
	assert.Equal(t, sha256Digest(t, "d"), byType[store.ArtifactSignature])
	assert.Equal(t, sha256Digest(t, "e"), byType[store.ArtifactSBOM])
	assert.Equal(t, sha256Digest(t, "f"), byType[store.ArtifactProvenance])
}

// AC-011-06: a (digest, type) pair appears once, and an attached artifact's ref
// wins over the derived one.
func TestDeriveCandidatesDeduplicatesAndPrefersAttachedRefs(t *testing.T) {
	msg := validSubmitRequest(t)
	imageDigest := sha256Digest(t, "b")
	msg.Artifacts = []*commonv1.CandidateArtifact{{
		ArtifactType: commonv1.ArtifactType_ARTIFACT_TYPE_IMAGE,
		Ref:          "reg/app@sha256-pinned", Digest: imageDigest,
	}}

	candidates := deriveCandidates(msg)

	images := 0
	for _, candidate := range candidates {
		if candidate.ArtifactType == store.ArtifactImage && candidate.Digest == imageDigest {
			images++
			assert.Equal(t, "reg/app@sha256-pinned", candidate.Ref,
				"AC-011-06: the attached ref wins over the derived one")
		}
	}
	assert.Equal(t, 1, images, "AC-011-06: the duplicate (digest, type) pair collapses to one candidate")
}
