package webhook

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	webhookv1 "github.com/ndzuki/release-manager/api/gen/webhook/v1"
	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
)

// Service implements the WebhookServiceHandler Connect interface.
type Service struct {
	logger       *slog.Logger
	bundleClient orchestratorv1connect.BundleServiceClient
	// serviceToken is the dev bundle-ingress service token (REQ-011 §562 dev
	// minimal wiring, D-100 选项 B, AC-065-33). When set it is forwarded to
	// the orchestrator BundleService as `Authorization: Bearer <token>` so
	// the orchestrator's ServiceTokenInterceptor authenticates the call as
	// actor service:release-webhook. Empty in production/non-dev runs.
	serviceToken string
}

// NewService creates a new webhook Connect service. serviceToken is the
// optional bundle-ingress service token forwarded to the orchestrator.
func NewService(logger *slog.Logger, bundleClient orchestratorv1connect.BundleServiceClient, serviceToken string) *Service {
	return &Service{logger: logger, bundleClient: bundleClient, serviceToken: serviceToken}
}

// SubmitReleaseBundle validates and forwards a bundle from CI to orchestrator.
func (s *Service) SubmitReleaseBundle(
	ctx context.Context,
	req *connect.Request[webhookv1.SubmitReleaseBundleRequest],
) (*connect.Response[webhookv1.SubmitReleaseBundleResponse], error) {
	if s.bundleClient == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("orchestrator client is not configured"))
	}
	msg := req.Msg
	orchestratorReq := connect.NewRequest(&orchestratorv1.SubmitBundleRequest{
		Name:         msg.GetName(),
		ChartRef:     msg.GetChartRef(),
		ChartVersion: msg.GetChartVersion(),
		ChartDigest:  msg.GetChartDigest(),
		Images:       msg.GetImages(),
		GitCommit:    msg.GetGitCommit(),
		PipelineId:   msg.GetPipelineId(),
		Signature:    msg.GetSignature(),
		Sbom:         msg.GetSbom(),
		Provenance:   msg.GetProvenance(),
		Artifacts:    msg.GetArtifacts(),
	})
	if key := req.Header().Get("Idempotency-Key"); key != "" {
		orchestratorReq.Header().Set("Idempotency-Key", key)
	}
	// D-100 选项 B: forward the dev service token so the orchestrator's
	// ServiceTokenInterceptor can authenticate the bundle ingress as
	// service:release-webhook. No token configured → header stays absent
	// (production path unchanged).
	if s.serviceToken != "" {
		orchestratorReq.Header().Set("Authorization", "Bearer "+s.serviceToken)
	}
	resp, err := s.bundleClient.SubmitBundle(ctx, orchestratorReq)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&webhookv1.SubmitReleaseBundleResponse{
		Bundle:  resp.Msg.GetBundle(),
		Created: resp.Msg.GetCreated(),
	}), nil
}

// Compile-time check: Service implements the Connect handler interface.
var _ webhookv1connect.WebhookServiceHandler = (*Service)(nil)
