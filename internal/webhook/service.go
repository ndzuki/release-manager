// Package webhook implements artifact ingestion from external sources.
package webhook

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	webhookv1 "github.com/ndzuki/release-manager/api/gen/webhook/v1"
	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

// Service implements the WebhookServiceHandler Connect interface.
type Service struct {
	store  store.Store
	logger *slog.Logger
}

// NewService creates a new webhook Connect service.
func NewService(st store.Store, logger *slog.Logger) *Service {
	return &Service{store: st, logger: logger}
}

// SubmitReleaseBundle validates and persists a release bundle from CI.
func (s *Service) SubmitReleaseBundle(
	ctx context.Context,
	req *connect.Request[webhookv1.SubmitReleaseBundleRequest],
) (*connect.Response[webhookv1.SubmitReleaseBundleResponse], error) {
	msg := req.Msg

	if err := validateSubmitRequest(msg); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	bundleDigest := computeBundleDigest(msg)

	// Idempotency: return existing bundle if same digest was already submitted.
	existing, err := s.store.Bundles().GetByDigest(ctx, "sha256", bundleDigest)
	if err == nil {
		s.logger.Info("idempotent bundle submission", "bundle_id", existing.ID, "digest", bundleDigest)
		return connect.NewResponse(&webhookv1.SubmitReleaseBundleResponse{
			Bundle: toProtoBundle(existing),
		}), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("bundle digest lookup: %w", err))
	}

	// Build domain bundle.
	now := time.Now().UTC()
	images := make([]store.BundleImage, 0, len(msg.Images))
	for _, img := range msg.Images {
		images = append(images, store.BundleImage{
			Ref:        img.Ref,
			Digest:     img.Digest,
			ValuesPath: img.ValuesPath,
		})
	}

	b := &store.ReleaseBundle{
		ID:            uuid.New().String(),
		Name:          msg.Name,
		DigestAlg:     "sha256",
		DigestValue:   bundleDigest,
		Status:        store.BundleReceived,
		ChartRef:      msg.ChartRef,
		ChartVersion:  msg.ChartVersion,
		ChartDigest:   msg.ChartDigest,
		Images:        images,
		GitCommit:     msg.GitCommit,
		PipelineID:    msg.PipelineId,
		SignatureRef:  msg.SignatureRef,
		SBOMRef:       msg.SbomRef,
		ProvenanceRef: msg.ProvenanceRef,
		CreatedAt:     now,
	}

	if err := s.store.Bundles().Create(ctx, b); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create bundle: %w", err))
	}

	s.logger.Info("release bundle created",
		"bundle_id", b.ID,
		"name", b.Name,
		"digest", bundleDigest,
	)

	return connect.NewResponse(&webhookv1.SubmitReleaseBundleResponse{
		Bundle: toProtoBundle(b),
	}), nil
}

// IngestArtifact records a raw artifact event (e.g. Harbor webhook) without
// triggering any release pipeline.
func (s *Service) IngestArtifact(
	ctx context.Context,
	req *connect.Request[webhookv1.IngestArtifactRequest],
) (*connect.Response[webhookv1.IngestArtifactResponse], error) {
	msg := req.Msg

	now := time.Now().UTC()

	audit := &store.AuditEvent{
		ID:             uuid.New().String(),
		ActorKind:      store.AuditActorSystem,
		ActorID:        "harbor-webhook",
		OrganizationID: "",
		Role:           "",
		ResourceType:   "artifact",
		ResourceID:     msg.ArtifactUrl,
		Action:         "ingest",
		Status:         "recorded",
		DurationMs:     0,
		ChangeSummary: fmt.Sprintf("source=%s type=%s", msg.Source, msg.ArtifactType),
		Metadata:       msg.Metadata,
		CreatedAt:      now,
	}

	if err := s.store.AuditEvents().Create(ctx, audit); err != nil {
		s.logger.Error("failed to record artifact event", "err", err)
		// Non-fatal: we still return OK so the webhook doesn't retry.
	}

	s.logger.Info("artifact event recorded",
		"source", msg.Source,
		"type", msg.ArtifactType,
		"url", msg.ArtifactUrl,
	)

	// IngestArtifact does NOT create an Operation or trigger PublishRelease.
	// It only records the raw event for audit purposes.
	return connect.NewResponse(&webhookv1.IngestArtifactResponse{
		Bundle: nil,
	}), nil
}

// validateSubmitRequest performs schema-level validation.
func validateSubmitRequest(msg *webhookv1.SubmitReleaseBundleRequest) error {
	var errs []string

	if msg.ChartDigest == "" {
		errs = append(errs, "chart_digest is required")
	}

	seenPaths := make(map[string]bool)
	for i, img := range msg.Images {
		if img.Digest == "" {
			errs = append(errs, fmt.Sprintf("images[%d].digest is required", i))
		}
		if img.ValuesPath == "" {
			errs = append(errs, fmt.Sprintf("images[%d].values_path is required", i))
		}
		if img.ValuesPath != "" {
			if seenPaths[img.ValuesPath] {
				errs = append(errs, fmt.Sprintf("images[%d]: duplicate values_path %q", i, img.ValuesPath))
			}
			seenPaths[img.ValuesPath] = true
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// computeBundleDigest produces a SHA-256 digest of the content-addressable bundle.
// Algorithm: SHA-256(chart_ref|chart_version|chart_digest|sorted_image_digests|sorted_metadata)
func computeBundleDigest(msg *webhookv1.SubmitReleaseBundleRequest) string {
	var parts []string

	parts = append(parts, msg.ChartRef, msg.ChartVersion, msg.ChartDigest)

	// Sort image digests for deterministic hashing.
	imgParts := make([]string, 0, len(msg.Images))
	for _, img := range msg.Images {
		imgParts = append(imgParts, fmt.Sprintf("%s|%s|%s", img.Ref, img.Digest, img.ValuesPath))
	}
	sort.Strings(imgParts)
	parts = append(parts, imgParts...)

	// Metadata.
	parts = append(parts, msg.GitCommit, msg.PipelineId,
		msg.SignatureRef, msg.SbomRef, msg.ProvenanceRef)

	payload := strings.Join(parts, "\n")
	h := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", h)
}

// toProtoBundle converts a domain ReleaseBundle to its proto representation.
func toProtoBundle(b *store.ReleaseBundle) *commonv1.ReleaseBundle {
	images := make([]*commonv1.BundleImage, 0, len(b.Images))
	for _, img := range b.Images {
		images = append(images, &commonv1.BundleImage{
			Ref:        img.Ref,
			Digest:     img.Digest,
			ValuesPath: img.ValuesPath,
		})
	}

	return &commonv1.ReleaseBundle{
		Id:   b.ID,
		Name: b.Name,
		Digest: &commonv1.ReleaseDigest{
			Algorithm: b.DigestAlg,
			Value:     b.DigestValue,
		},
		Status:        commonv1.BundleStatus(commonv1.BundleStatus_value["BUNDLE_STATUS_"+strings.ToUpper(string(b.Status))]),
		ChartRef:      b.ChartRef,
		ChartVersion:  b.ChartVersion,
		ChartDigest:   b.ChartDigest,
		Images:        images,
		GitCommit:     b.GitCommit,
		PipelineId:    b.PipelineID,
		SignatureRef:  b.SignatureRef,
		SbomRef:       b.SBOMRef,
		ProvenanceRef: b.ProvenanceRef,
		CreatedAt:     timestamppb.New(b.CreatedAt),
	}
}

// Compile-time check: Service implements the Connect handler interface.
var _ webhookv1connect.WebhookServiceHandler = (*Service)(nil)
