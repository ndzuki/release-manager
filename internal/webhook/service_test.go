package webhook

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	webhookv1 "github.com/ndzuki/release-manager/api/gen/webhook/v1"
)

type stubBundleClient struct {
	orchestratorv1connect.BundleServiceClient
	submitResponse func() (*orchestratorv1.SubmitBundleResponse, error)
}

func (c *stubBundleClient) SubmitBundle(_ context.Context, _ *connect.Request[orchestratorv1.SubmitBundleRequest]) (*connect.Response[orchestratorv1.SubmitBundleResponse], error) {
	resp, err := c.submitResponse()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func setupService(t *testing.T) *Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	client := &stubBundleClient{
		submitResponse: func() (*orchestratorv1.SubmitBundleResponse, error) {
			return &orchestratorv1.SubmitBundleResponse{
				Bundle:  &orchestratorv1.BundleSummary{Id: "bundle-001", Name: "test-release"},
				Created: true,
			}, nil
		},
	}
	return NewService(logger, client)
}

func TestSubmitReleaseBundle_ForwardsToOrchestrator(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()
	req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
		Name: "test-release", ChartRef: "oci://registry.example.com/charts/myapp",
		ChartVersion: "1.2.3", ChartDigest: "sha256:abc123",
	})
	resp, err := svc.SubmitReleaseBundle(ctx, req)
	require.NoError(t, err)
	assert.NotNil(t, resp.Msg.GetBundle())
	assert.True(t, resp.Msg.GetCreated())
}

func TestSubmitReleaseBundle_NoClient(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc := NewService(logger, nil)
	ctx := context.Background()
	req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{Name: "test", ChartDigest: "sha256:abc123"})
	_, err := svc.SubmitReleaseBundle(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}
