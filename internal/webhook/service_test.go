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

// stubBundleClient records the forwarded request so tests can assert the
// Authorization / Idempotency-Key headers at the forwarding boundary
// (AC-065-33: webhook passes `Authorization: Bearer <dev-service-token>`).
type stubBundleClient struct {
	orchestratorv1connect.BundleServiceClient
	submitResponse func() (*orchestratorv1.SubmitBundleResponse, error)
	lastReq        *connect.Request[orchestratorv1.SubmitBundleRequest]
}

func (c *stubBundleClient) SubmitBundle(_ context.Context, req *connect.Request[orchestratorv1.SubmitBundleRequest]) (*connect.Response[orchestratorv1.SubmitBundleResponse], error) {
	c.lastReq = req
	resp, err := c.submitResponse()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func setupService(t *testing.T, serviceToken string) (*Service, *stubBundleClient) {
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
	return NewService(logger, client, serviceToken), client
}

func TestSubmitReleaseBundle_ForwardsToOrchestrator(t *testing.T) {
	svc, _ := setupService(t, "")
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
	svc := NewService(logger, nil, "")
	ctx := context.Background()
	req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{Name: "test", ChartDigest: "sha256:abc123"})
	_, err := svc.SubmitReleaseBundle(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

// TestSubmitReleaseBundle_ForwardsServiceToken covers AC-065-33 (sender
// side): with a dev service token configured the forwarded request carries
// `Authorization: Bearer <token>` alongside the Idempotency-Key; without a
// token only the Idempotency-Key is forwarded (production path unchanged).
func TestSubmitReleaseBundle_ForwardsServiceToken(t *testing.T) {
	t.Run("with token", func(t *testing.T) {
		svc, client := setupService(t, "dev-service-token")
		req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
			Name: "test-release", ChartDigest: "sha256:abc123",
		})
		req.Header().Set("Idempotency-Key", "key-1")
		_, err := svc.SubmitReleaseBundle(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, client.lastReq)
		assert.Equal(t, "Bearer dev-service-token", client.lastReq.Header().Get("Authorization"))
		assert.Equal(t, "key-1", client.lastReq.Header().Get("Idempotency-Key"))
	})

	t.Run("without token", func(t *testing.T) {
		svc, client := setupService(t, "")
		req := connect.NewRequest(&webhookv1.SubmitReleaseBundleRequest{
			Name: "test-release", ChartDigest: "sha256:abc123",
		})
		req.Header().Set("Idempotency-Key", "key-2")
		_, err := svc.SubmitReleaseBundle(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, client.lastReq)
		assert.Empty(t, client.lastReq.Header().Get("Authorization"))
		assert.Equal(t, "key-2", client.lastReq.Header().Get("Idempotency-Key"))
	})
}
