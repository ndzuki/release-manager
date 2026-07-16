package webhook

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	webhookv1 "github.com/ndzuki/release-manager/api/gen/webhook/v1"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func setupService(t *testing.T) *Service {
	t.Helper()
	st, err := sqlitestore.Open("file::memory:?cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewService(st, logger)
}

func TestSubmitReleaseBundle_Success(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
		Name:         "test-release",
		ChartRef:     "oci://registry.example.com/charts/myapp",
		ChartVersion: "1.2.3",
		ChartDigest:  "sha256:abc123",
		Images: []*commonv1.BundleImage{
			{
				Ref:        "registry.example.com/myapp",
				Digest:     "sha256:def456",
				ValuesPath: "image.tag",
			},
			{
				Ref:        "registry.example.com/sidecar",
				Digest:     "sha256:ghi789",
				ValuesPath: "sidecar.image.repository",
			},
		},
		GitCommit:  "abc123def",
		PipelineId: "pipeline-42",
	})

	resp, err := svc.SubmitReleaseBundle(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Bundle)

	b := resp.Msg.Bundle
	assert.NotEmpty(t, b.Id)
	assert.Equal(t, "test-release", b.Name)
	assert.NotEmpty(t, b.Digest.Value)
	assert.Equal(t, "sha256", b.Digest.Algorithm)
	assert.Equal(t, commonv1.BundleStatus_BUNDLE_STATUS_RECEIVED, b.Status)
	assert.Equal(t, "oci://registry.example.com/charts/myapp", b.ChartRef)
	assert.Equal(t, "1.2.3", b.ChartVersion)
	assert.Equal(t, "sha256:abc123", b.ChartDigest)
	require.Len(t, b.Images, 2)
	assert.Equal(t, "image.tag", b.Images[0].ValuesPath)
	assert.Equal(t, "sidecar.image.repository", b.Images[1].ValuesPath)
	assert.Equal(t, "abc123def", b.GitCommit)
	assert.Equal(t, "pipeline-42", b.PipelineId)
}

func TestSubmitReleaseBundle_Idempotent(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
		Name:         "test-release",
		ChartRef:     "oci://registry.example.com/charts/myapp",
		ChartVersion: "1.0.0",
		ChartDigest:  "sha256:aaa",
		Images: []*commonv1.BundleImage{
			{
				Ref:        "registry.example.com/app",
				Digest:     "sha256:bbb",
				ValuesPath: "image.tag",
			},
		},
		GitCommit:  "commit1",
		PipelineId: "pipe-1",
	})

	// First submission.
	resp1, err := svc.SubmitReleaseBundle(ctx, req)
	require.NoError(t, err)

	// Second submission with identical content.
	resp2, err := svc.SubmitReleaseBundle(ctx, req)
	require.NoError(t, err)

	// Should return the same bundle (idempotent).
	assert.Equal(t, resp1.Msg.Bundle.Id, resp2.Msg.Bundle.Id)
	assert.Equal(t, resp1.Msg.Bundle.Digest.Value, resp2.Msg.Bundle.Digest.Value)
}

func TestSubmitReleaseBundle_IdenticalDigestDiffersIfContentDiffers(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	req1 := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
		Name:         "release-a",
		ChartRef:     "oci://charts/a",
		ChartVersion: "1.0.0",
		ChartDigest:  "sha256:same",
		Images: []*commonv1.BundleImage{
			{
				Ref:        "img",
				Digest:     "sha256:same",
				ValuesPath: "a.b",
			},
		},
		GitCommit:  "abc",
		PipelineId: "1",
	})

	req2 := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
		Name:         "release-b",
		ChartRef:     "oci://charts/a",
		ChartVersion: "1.0.0",
		ChartDigest:  "sha256:same",
		Images: []*commonv1.BundleImage{
			{
				Ref:        "img",
				Digest:     "sha256:same",
				ValuesPath: "x.y",
			},
		},
		GitCommit:  "abc",
		PipelineId: "1",
	})

	resp1, err := svc.SubmitReleaseBundle(ctx, req1)
	require.NoError(t, err)

	resp2, err := svc.SubmitReleaseBundle(ctx, req2)
	require.NoError(t, err)

	// Different values_path should produce different digests -> different bundles.
	assert.NotEqual(t, resp1.Msg.Bundle.Id, resp2.Msg.Bundle.Id)
	assert.NotEqual(t, resp1.Msg.Bundle.Digest.Value, resp2.Msg.Bundle.Digest.Value)
}

func TestSubmitReleaseBundle_MissingChartDigest(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
		Name:         "bad-release",
		ChartRef:     "oci://charts/x",
		ChartVersion: "1.0.0",
		ChartDigest:  "", // missing
		Images: []*commonv1.BundleImage{
			{
				Ref:        "img",
				Digest:     "sha256:abc",
				ValuesPath: "image.tag",
			},
		},
	})

	_, err := svc.SubmitReleaseBundle(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chart_digest")
}

func TestSubmitReleaseBundle_MissingImageDigest(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
		Name:         "bad-release",
		ChartRef:     "oci://charts/x",
		ChartVersion: "1.0.0",
		ChartDigest:  "sha256:abc",
		Images: []*commonv1.BundleImage{
			{
				Ref:        "img",
				Digest:     "", // missing
				ValuesPath: "image.tag",
			},
		},
	})

	_, err := svc.SubmitReleaseBundle(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "digest is required")
}

func TestSubmitReleaseBundle_DuplicateValuesPath(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
		Name:         "dup-paths",
		ChartRef:     "oci://charts/x",
		ChartVersion: "1.0.0",
		ChartDigest:  "sha256:abc",
		Images: []*commonv1.BundleImage{
			{
				Ref:        "img1",
				Digest:     "sha256:aaa",
				ValuesPath: "image.tag",
			},
			{
				Ref:        "img2",
				Digest:     "sha256:bbb",
				ValuesPath: "image.tag", // duplicate
			},
		},
	})

	_, err := svc.SubmitReleaseBundle(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate values_path")
}

func TestIngestArtifact_NoPublish(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	req := connect.NewRequest(&webhookv1.IngestArtifactRequest{
		Source:       "harbor",
		ArtifactUrl:  "harbor.example.com/project/image:v1.0.0",
		ArtifactType: "image",
		Metadata: map[string]string{
			"action": "push",
		},
	})

	resp, err := svc.IngestArtifact(ctx, req)
	require.NoError(t, err)
	// IngestArtifact should NOT return a bundle (no publish triggered).
	assert.Nil(t, resp.Msg.Bundle)
}
