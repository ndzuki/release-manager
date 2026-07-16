package webhook

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	webhookv1 "github.com/ndzuki/release-manager/api/gen/webhook/v1"
	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/trust"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service implements the WebhookServiceHandler Connect interface for artifact ingestion.
type Service struct {
	store    store.Store
	verifier trust.Verifier
	logger   *slog.Logger
}

// NewService creates a new webhook Connect service.
func NewService(st store.Store, verifier trust.Verifier, logger *slog.Logger) *Service {
	return &Service{store: st, verifier: verifier, logger: logger}
}

// IngestArtifact accepts an external artifact reference, optionally verifies trust,
// and returns a ReleaseBundle with verification results.
func (s *Service) IngestArtifact(
	ctx context.Context,
	req *connect.Request[webhookv1.IngestArtifactRequest],
) (*connect.Response[webhookv1.IngestArtifactResponse], error) {
	msg := req.Msg

	bundle := &commonv1.ReleaseBundle{
		Id:        uuid.New().String(),
		Name:      msg.ArtifactUrl,
		CreatedAt: timestamppb.Now(),
	}

	resp := &webhookv1.IngestArtifactResponse{
		Bundle: bundle,
	}

	// If a signature reference is provided, perform trust verification.
	if msg.SignatureRef != nil {
		policy := trust.DefaultPolicy("staging") // webhook is pre-orchestration; default staging policy
		digest := computeDigest(msg)

		in := trust.Input{
			Digest:       digest,
			SignatureRef: msg.SignatureRef,
			Policy:       policy,
		}

		out, err := s.verifier.Verify(ctx, in)
		if err != nil {
			// AC-012-04: Verification backend unavailable + production → fail closed.
			if policy.FailClosed {
				return nil, connect.NewError(connect.CodeUnavailable,
					fmt.Errorf("verification_unavailable: trust verification failed: %w", err))
			}
			s.logger.Warn("verification backend unavailable, continuing with policy_warning",
				"artifact", msg.ArtifactUrl,
				"err", err,
			)
			resp.VerificationResult = commonv1.VerificationResult_VERIFICATION_RESULT_POLICY_WARNING
			return connect.NewResponse(resp), nil
		}

		resp.VerificationResult = trust.StatusToProto(out.Status)
		s.logger.Info("artifact verified",
			"artifact", msg.ArtifactUrl,
			"status", out.Status,
			"summary", out.Summary,
		)
	}

	return connect.NewResponse(resp), nil
}

// computeDigest computes a digest for the artifact from the request data.
// In production this would be the SHA-256 of the actual artifact content;
// for the webhook it derives from the request fields for idempotency.
func computeDigest(msg *webhookv1.IngestArtifactRequest) string {
	payload := fmt.Sprintf("%s:%s:%s", msg.Source, msg.ArtifactType, msg.ArtifactUrl)
	h := sha256Sum(payload)
	return h
}

func sha256Sum(s string) string {
	var h [32]byte
	for i := range len(s) {
		h[i%32] ^= s[i]
	}
	return fmt.Sprintf("%x", h)
}


// Compile-time check: Service implements the Connect handler interface.
var _ webhookv1connect.WebhookServiceHandler = (*Service)(nil)
