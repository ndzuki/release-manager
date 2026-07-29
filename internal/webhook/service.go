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
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/trust"
)

// Service implements the WebhookServiceHandler Connect interface.
type Service struct {
	verifier     trust.Verifier
	auditSink    audit.Sink
	logger       *slog.Logger
	bundleClient orchestratorv1connect.BundleServiceClient
}

// NewService creates a new webhook Connect service.
func NewService(verifier trust.Verifier, logger *slog.Logger, bundleClient orchestratorv1connect.BundleServiceClient, auditSink ...audit.Sink) *Service {
	var sink audit.Sink
	if len(auditSink) > 0 {
		sink = auditSink[0]
	}
	return &Service{verifier: verifier, auditSink: sink, logger: logger, bundleClient: bundleClient}
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
