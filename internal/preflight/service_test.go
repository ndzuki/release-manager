package preflight

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/trust"
)

// ---------- test helpers ----------

type stubPreflightStore struct {
	records map[string]*store.PreflightRecord
}

func newStubPreflightStore() *stubPreflightStore {
	return &stubPreflightStore{records: map[string]*store.PreflightRecord{}}
}

func (s *stubPreflightStore) key(rec *store.PreflightRecord) string {
	return rec.Key.OperationID + "\x00" + rec.Key.RoutingVersion + "\x00" +
		rec.Key.BundleDigest + "\x00" + rec.Key.TrustPolicyVersion + "\x00" + rec.Key.SBOMPolicyVersion
}

func (s *stubPreflightStore) Create(_ context.Context, rec *store.PreflightRecord) error {
	k := s.key(rec)
	if _, ok := s.records[k]; ok {
		return nil // DO NOTHING
	}
	s.records[k] = rec
	return nil
}

func (s *stubPreflightStore) GetByKey(_ context.Context, key store.PreflightCacheKey) (*store.PreflightRecord, error) {
	k := key.OperationID + "\x00" + key.RoutingVersion + "\x00" +
		key.BundleDigest + "\x00" + key.TrustPolicyVersion + "\x00" + key.SBOMPolicyVersion
	rec, ok := s.records[k]
	if !ok {
		return nil, store.ErrNotFound
	}
	return rec, nil
}

type stubResolver struct {
	digest string
	err    error
}

func (r *stubResolver) ResolveDigest(_ context.Context, _ store.ArtifactType, _ string) (string, error) {
	return r.digest, r.err
}

type stubVerifier struct {
	status  store.VerificationStatus
	summary string
	err     error
}

func (v *stubVerifier) Verify(_ context.Context, _ trust.Input) (*trust.Output, error) {
	if v.err != nil {
		return nil, v.err
	}
	return &trust.Output{
		Status:  v.status,
		Summary: v.summary,
	}, nil
}

func bundleFixture(images []store.BundleImage) *store.ReleaseBundle {
	return &store.ReleaseBundle{
		ID:          "bundle-001",
		DigestAlg:   "sha256",
		DigestValue: "bundle-digest",
		ChartRef:    "charts.helm.sh/stable/nginx",
		ChartDigest: "sha256:abc123",
		Images:      images,
	}
}

func routesFixture() []*store.ClusterRoute {
	return []*store.ClusterRoute{
		{
			ID:           "r-chart",
			ClusterID:    "cls-001",
			ArtifactType: store.ArtifactChart,
			Mode:         store.ModeDirect,
			SourcePrefix: "charts.helm.sh/stable/",
			TargetPrefix: "registry.internal/charts/",
		},
		{
			ID:           "r-img",
			ClusterID:    "cls-001",
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeReplicated,
			SourcePrefix: "docker.io/myorg/",
			TargetPrefix: "oci://registry.internal/replicated/",
		},
	}
}

func signRef() *commonv1.SignatureRef {
	return &commonv1.SignatureRef{
		Digest:    "sha256:digest-verified",
		Signature: "MEUCIQD...",
		Issuer:    "release-manager-ci",
		Subject:   "release-manager/v1.0.0",
	}
}

func discardLog() Logger { return discardLogger{} }

// ---------- tests ----------

// AC-045-03: Multi-image bundle — each image independently verified.
func TestRun_MultiImage(t *testing.T) {
	bundle := bundleFixture([]store.BundleImage{
		{Ref: "docker.io/myorg/app", Digest: "sha256:abc123"},
		{Ref: "docker.io/myorg/sidecar", Digest: "sha256:abc123"},
	})
	st := newStubPreflightStore()
	resolver := &stubResolver{digest: "sha256:abc123"}
	verifier := &stubVerifier{status: store.VerificationTrusted, summary: "trusted"}
	svc := New(st, verifier, resolver, discardLog())

	out, err := svc.Run(context.Background(), Input{
		OperationID:       "op-001",
		ClusterID:         "cls-001",
		Bundle:            bundle,
		Routes:            routesFixture(),
		TrustPolicy:       store.TrustPolicy{PolicyVersion: "v1"},
		SBOMPolicyVersion: "",
		SignatureRef:      signRef(),
	})
	require.NoError(t, err)
	assert.False(t, out.Reused)
	assert.Len(t, out.Results, 3) // 1 chart + 2 images
	assert.True(t, out.Passed)
	assert.Equal(t, store.ArtifactChart, out.Results[0].Type)
	assert.Equal(t, store.ArtifactImage, out.Results[1].Type)
	assert.Equal(t, store.ArtifactImage, out.Results[2].Type)

	for _, r := range out.Results {
		assert.Equal(t, ErrorNone, r.ErrorCode)
		assert.True(t, r.DigestParity)
		assert.GreaterOrEqual(t, r.DurationMS, int64(0))
	}
}

// AC-045-04: Idempotent reuse — same input returns cached result.
func TestRun_IdempotentReuse(t *testing.T) {
	bundle := bundleFixture(nil) // chart-only
	st := newStubPreflightStore()
	resolver := &stubResolver{digest: "sha256:abc123"}
	verifier := &stubVerifier{status: store.VerificationTrusted}
	svc := New(st, verifier, resolver, discardLog())

	in := Input{
		OperationID:       "op-002",
		ClusterID:         "cls-001",
		Bundle:            bundle,
		Routes:            routesFixture(),
		TrustPolicy:       store.TrustPolicy{PolicyVersion: "v1"},
		SBOMPolicyVersion: "",
	}

	out1, err := svc.Run(context.Background(), in)
	require.NoError(t, err)
	assert.False(t, out1.Reused)
	assert.True(t, out1.Passed)

	out2, err := svc.Run(context.Background(), in)
	require.NoError(t, err)
	assert.True(t, out2.Reused, "second call must reuse cached result")
	assert.Equal(t, out1.Passed, out2.Passed)
}
// AC-045-01: Digest mismatch → digest_mismatch.
func TestRun_DigestMismatch(t *testing.T) {
	bundle := bundleFixture(nil) // chart-only, expected digest sha256:abc123
	st := newStubPreflightStore()
	resolver := &stubResolver{digest: "sha256:unexpected-digest"}
	verifier := &stubVerifier{status: store.VerificationTrusted}
	svc := New(st, verifier, resolver, discardLog())

	out, err := svc.Run(context.Background(), Input{
		OperationID:       "op-003",
		ClusterID:         "cls-001",
		Bundle:            bundle,
		Routes:            routesFixture(),
		TrustPolicy:       store.TrustPolicy{PolicyVersion: "v1"},
		SBOMPolicyVersion: "",
	})
	require.NoError(t, err)
	assert.False(t, out.Passed)
	assert.Equal(t, ErrorDigestMismatch, out.Results[0].ErrorCode)
	assert.False(t, out.Results[0].DigestParity)
}

// AC-045-02: No route rules → routing_no_match, no fallback to direct.
func TestRun_RoutingNoMatch(t *testing.T) {
	bundle := bundleFixture(nil)
	st := newStubPreflightStore()
	resolver := &stubResolver{}
	verifier := &stubVerifier{}
	svc := New(st, verifier, resolver, discardLog())

	out, err := svc.Run(context.Background(), Input{
		OperationID:       "op-004",
		ClusterID:         "cls-002",
		Bundle:            bundle,
		Routes:            nil, // no routes → routing_no_match
		TrustPolicy:       store.TrustPolicy{PolicyVersion: "v1"},
		SBOMPolicyVersion: "",
	})
	require.NoError(t, err)
	assert.False(t, out.Passed)
	assert.Equal(t, ErrorRoutingNoMatch, out.Results[0].ErrorCode)
}

// AC-045-03: Signature invalid → signature_invalid.
func TestRun_SignatureInvalid(t *testing.T) {
	bundle := bundleFixture(nil)
	st := newStubPreflightStore()
	resolver := &stubResolver{digest: "sha256:abc123"}
	verifier := &stubVerifier{status: store.VerificationRejected, summary: "signature_invalid"}
	svc := New(st, verifier, resolver, discardLog())

	out, err := svc.Run(context.Background(), Input{
		OperationID:       "op-005",
		ClusterID:         "cls-001",
		Bundle:            bundle,
		Routes:            routesFixture(),
		TrustPolicy:       store.TrustPolicy{PolicyVersion: "v1"},
		SBOMPolicyVersion: "",
	})
	require.NoError(t, err)
	assert.False(t, out.Passed)
	assert.Equal(t, ErrorSignatureInvalid, out.Results[0].ErrorCode)
}

// Missing operation ID must be rejected.
func TestRun_MissingOperationID(t *testing.T) {
	svc := New(nil, nil, nil, discardLog())
	_, err := svc.Run(context.Background(), Input{
		ClusterID: "cls-001",
		Bundle:    bundleFixture(nil),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "operation id is required")
}

// Missing cluster ID must be rejected.
func TestRun_MissingClusterID(t *testing.T) {
	svc := New(nil, nil, nil, discardLog())
	_, err := svc.Run(context.Background(), Input{
		OperationID: "op-001",
		Bundle:      bundleFixture(nil),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cluster id is required")
}

// Missing bundle must be rejected.
func TestRun_MissingBundle(t *testing.T) {
	svc := New(nil, nil, nil, discardLog())
	_, err := svc.Run(context.Background(), Input{
		OperationID: "op-001",
		ClusterID:   "cls-001",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "release bundle is required")
}

// Empty bundle digest must be rejected.
func TestRun_EmptyBundleDigest(t *testing.T) {
	svc := New(nil, nil, nil, discardLog())
	_, err := svc.Run(context.Background(), Input{
		OperationID: "op-001",
		ClusterID:   "cls-001",
		Bundle: &store.ReleaseBundle{
			ID:          "bundle-001",
			DigestValue: "",
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "release bundle digest is required")
}

// Artifact not found in registry.
func TestRun_ArtifactNotFound(t *testing.T) {
	bundle := bundleFixture(nil)
	st := newStubPreflightStore()
	resolver := &stubResolver{err: ErrArtifactNotFound}
	verifier := &stubVerifier{}
	svc := New(st, verifier, resolver, discardLog())

	out, err := svc.Run(context.Background(), Input{
		OperationID:       "op-006",
		ClusterID:         "cls-001",
		Bundle:            bundle,
		Routes:            routesFixture(),
		TrustPolicy:       store.TrustPolicy{PolicyVersion: "v1"},
		SBOMPolicyVersion: "",
	})
	require.NoError(t, err)
	assert.False(t, out.Passed)
	assert.Equal(t, ErrorArtifactNotFound, out.Results[0].ErrorCode)
}

// Registry unauthorized.
func TestRun_RegistryUnauthorized(t *testing.T) {
	bundle := bundleFixture(nil)
	st := newStubPreflightStore()
	resolver := &stubResolver{err: ErrRegistryUnauthorized}
	verifier := &stubVerifier{}
	svc := New(st, verifier, resolver, discardLog())

	out, err := svc.Run(context.Background(), Input{
		OperationID:       "op-007",
		ClusterID:         "cls-001",
		Bundle:            bundle,
		Routes:            routesFixture(),
		TrustPolicy:       store.TrustPolicy{PolicyVersion: "v1"},
		SBOMPolicyVersion: "",
	})
	require.NoError(t, err)
	assert.False(t, out.Passed)
	assert.Equal(t, ErrorRegistryUnauthorized, out.Results[0].ErrorCode)
}

// Routing version changes invalidate cache.
func TestRun_RoutingVersionInvalidatesCache(t *testing.T) {
	bundle := bundleFixture(nil)
	st := newStubPreflightStore()
	resolver := &stubResolver{digest: "sha256:abc123"}
	verifier := &stubVerifier{status: store.VerificationTrusted}
	svc := New(st, verifier, resolver, discardLog())

	in1 := Input{
		OperationID:       "op-008",
		ClusterID:         "cls-001",
		Bundle:            bundle,
		Routes:            routesFixture(),
		TrustPolicy:       store.TrustPolicy{PolicyVersion: "v1"},
		SBOMPolicyVersion: "",
	}

	out1, err := svc.Run(context.Background(), in1)
	require.NoError(t, err)
	assert.False(t, out1.Reused)

	// Change routes — routing version must differ.
	in2 := Input{
		OperationID:       "op-008",
		ClusterID:         "cls-001",
		Bundle:            bundle,
		Routes: []*store.ClusterRoute{{
			ID:           "r-other",
			ClusterID:    "cls-001",
			ArtifactType: store.ArtifactChart,
			Mode:         store.ModeDirect,
			SourcePrefix: "other.io/",
			TargetPrefix: "reg.io/",
		}},
		TrustPolicy:       store.TrustPolicy{PolicyVersion: "v1"},
		SBOMPolicyVersion: "",
	}

	out2, err := svc.Run(context.Background(), in2)
	require.NoError(t, err)
	assert.False(t, out2.Reused, "different routing version must compute fresh")
	assert.NotEqual(t, out1.RoutingVersion, out2.RoutingVersion)
}

// Host allowlist rejects unlisted target.
func TestRun_HostNotAllowed(t *testing.T) {
	bundle := bundleFixture(nil)
	st := newStubPreflightStore()
	resolver := &stubResolver{}
	verifier := &stubVerifier{}
	svc := New(st, verifier, resolver, discardLog())

	out, err := svc.Run(context.Background(), Input{
		OperationID:       "op-009",
		ClusterID:         "cls-001",
		Bundle:            bundle,
		Routes:            routesFixture(),
		TrustPolicy:       store.TrustPolicy{PolicyVersion: "v1"},
		SBOMPolicyVersion: "",
		AllowedHosts:      []string{"trusted-registry.io"},
	})
	require.NoError(t, err)
	assert.False(t, out.Passed)
	assert.Equal(t, ErrorRoutingNoMatch, out.Results[0].ErrorCode)
}

// Output serialization round-trips through JSON.
func TestOutput_JSONRoundTrip(t *testing.T) {
	out := &Output{
		OperationID:    "op-010",
		RoutingVersion: "sha256:deadbeef",
		BundleDigest:   "digest",
		Results: []ArtifactResult{
			{
				Type:           store.ArtifactImage,
				Ref:            "docker.io/myorg/app",
				ExpectedDigest: "sha256:abc",
				ResolvedDigest: "sha256:abc",
				DigestParity:   true,
				ErrorCode:      ErrorNone,
				DurationMS:     42,
			},
		},
		Passed: true,
	}

	data, err := json.Marshal(out)
	require.NoError(t, err)

	var restored Output
	require.NoError(t, json.Unmarshal(data, &restored))
	assert.Equal(t, out.OperationID, restored.OperationID)
	assert.Equal(t, out.Passed, restored.Passed)
	assert.Len(t, restored.Results, 1)
}

// Duration is recorded for the overall run.
func TestRun_DurationRecorded(t *testing.T) {
	bundle := bundleFixture(nil)
	st := newStubPreflightStore()
	resolver := &stubResolver{digest: "sha256:abc123"}
	verifier := &stubVerifier{status: store.VerificationTrusted}
	svc := New(st, verifier, resolver, discardLog())

	out, err := svc.Run(context.Background(), Input{
		OperationID:       "op-011",
		ClusterID:         "cls-001",
		Bundle:            bundle,
		Routes:            routesFixture(),
		TrustPolicy:       store.TrustPolicy{PolicyVersion: "v1"},
		SBOMPolicyVersion: "",
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, out.DurationMS, int64(0))
	assert.False(t, time.Duration(out.DurationMS*int64(time.Millisecond)) > 10*time.Second,
		"preflight should complete in under 10 seconds")
}
