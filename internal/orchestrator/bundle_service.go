package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

var (
	sha256DigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	valuesPathPattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*)*$`)
)

// SourceRegistry grants ingestion and validation access to one registry prefix.
type SourceRegistry struct {
	Prefix        string
	CredentialRef string
	Insecure      bool
}

// BundleService implements the orchestrator BundleService Connect surface.
type BundleService struct {
	store            store.Store
	logger           *slog.Logger
	sourceRegistries []SourceRegistry
}

func NewBundleService(st store.Store, logger *slog.Logger, sourceRegistries []SourceRegistry) *BundleService {
	if logger == nil {
		logger = slog.Default()
	}
	return &BundleService{store: st, logger: logger, sourceRegistries: sourceRegistries}
}

func (s *BundleService) SubmitBundle(
	ctx context.Context,
	req *connect.Request[orchestratorv1.SubmitBundleRequest],
) (*connect.Response[orchestratorv1.SubmitBundleResponse], error) {
	if err := s.validateSubmitBundle(req.Msg); err != nil {
		return nil, err
	}
	bundle, candidates, err := bundleFromProto(req.Msg)
	if err != nil {
		return nil, err
	}
	requestHash := canonicalBundleDigest(req.Msg)
	bundle.DigestAlg = "sha256"
	bundle.DigestValue = requestHash
	bundle.Status = store.BundleReceived
	bundle.ID = uuid.NewString()
	bundle.CreatedAt = time.Now().UTC()

	var idempotency *store.IdempotencyRecord
	if key := req.Header().Get("Idempotency-Key"); key != "" {
		actor, _ := authctx.ActorFromContext(ctx)
		identity := actor.Service
		if identity == "" {
			identity = actor.UserID
		}
		idempotency = &store.IdempotencyRecord{
			Scope:       identity + ":SubmitBundle",
			Key:         key,
			RequestHash: requestHash,
			ExpiresAt:   time.Now().UTC().Add(24 * time.Hour),
		}
	}
	auditPayload, err := json.Marshal(map[string]string{
		"bundle_id": bundle.ID,
		"digest":    "sha256:" + requestHash,
	})
	if err != nil {
		return nil, internalBundleError("encode audit event", err)
	}
	createdBundle, created, err := s.store.BundleSubmissions().Submit(ctx, store.BundleSubmission{
		Bundle:      bundle,
		Candidates:  candidates,
		Idempotency: idempotency,
		Audit: &store.ApprovalOutboxEntry{
			ID: uuid.NewString(), EventType: "release_bundle.created", PayloadJSON: auditPayload, CreatedAt: bundle.CreatedAt,
		},
		ValidationEntry: &store.ValidationOutboxEntry{
			ID: uuid.NewString(), BundleID: bundle.ID, Status: store.ValidationPending,
			NextAttemptAt: bundle.CreatedAt, CreatedAt: bundle.CreatedAt, UpdatedAt: bundle.CreatedAt,
		},
	})
	if err != nil {
		if errors.Is(err, store.ErrIdempotencyConflict) {
			return nil, bundleError(connect.CodeAlreadyExists, "idempotency_conflict",
				errors.New("idempotency key conflict: different request for same scope and key"))
		}
		return nil, internalBundleError("submit bundle", err)
	}
	return connect.NewResponse(&orchestratorv1.SubmitBundleResponse{
		Bundle: bundleSummaryToProto(createdBundle), Created: created,
	}), nil
}

func (s *BundleService) RecordArtifactEvent(
	ctx context.Context,
	req *connect.Request[orchestratorv1.RecordArtifactEventRequest],
) (*connect.Response[orchestratorv1.RecordArtifactEventResponse], error) {
	msg := req.Msg
	if msg.GetSourceId() == "" || msg.GetEventId() == "" || msg.GetEventType() == "" || msg.GetRawPayload() == "" {
		return nil, bundleError(connect.CodeInvalidArgument, "missing_required_field", errors.New("artifact event required fields are missing"))
	}
	if len(msg.GetResources()) == 0 || len(msg.GetResources()) > 100 {
		return nil, bundleError(connect.CodeInvalidArgument, "invalid_artifact", errors.New("resources count must be between 1 and 100"))
	}
	if msg.GetArtifactType() == commonv1.ArtifactType_ARTIFACT_TYPE_UNSPECIFIED {
		return nil, bundleError(connect.CodeInvalidArgument, "invalid_artifact", errors.New("artifact_type is required"))
	}
	occurredAt, err := time.Parse(time.RFC3339, msg.GetOccurredAt())
	if err != nil {
		return nil, bundleError(connect.CodeInvalidArgument, "invalid_artifact", errors.New("occurred_at must be RFC3339"))
	}
	payloadHash := sha256.Sum256([]byte(msg.GetRawPayload()))
	if hex.EncodeToString(payloadHash[:]) != msg.GetPayloadSha256() {
		return nil, bundleError(connect.CodeInvalidArgument, "invalid_artifact", errors.New("payload_sha256 does not match raw_payload"))
	}

	artifactType := artifactTypeFromCommonProto(msg.GetArtifactType())
	candidates := make([]*store.CandidateArtifact, 0, len(msg.GetResources()))
	for index, resource := range msg.GetResources() {
		if !sha256DigestPattern.MatchString(resource.GetDigest()) || strings.TrimSpace(resource.GetRef()) == "" {
			return nil, bundleError(connect.CodeInvalidArgument, "invalid_artifact",
				fmt.Errorf("resources[%d] must include a valid digest and ref", index))
		}
		candidates = append(candidates, &store.CandidateArtifact{
			ArtifactType: artifactType, Ref: resource.GetRef(), Digest: resource.GetDigest(),
		})
	}
	now := time.Now().UTC()
	event := &store.ArtifactEvent{
		ID: uuid.NewString(), SourceID: msg.GetSourceId(), EventID: msg.GetEventId(), EventType: msg.GetEventType(),
		OccurredAt: occurredAt.UTC(), ReceivedAt: now, RawPayload: msg.GetRawPayload(),
		PayloadSHA256: msg.GetPayloadSha256(), ArtifactType: artifactType, Repository: msg.GetRepository(),
	}
	auditPayload, err := json.Marshal(map[string]string{"event_id": event.ID, "source_id": event.SourceID})
	if err != nil {
		return nil, internalBundleError("encode artifact event audit", err)
	}
	result, err := s.store.ArtifactEventSubmissions().Record(ctx, store.ArtifactEventSubmission{
		Event: event, Candidates: candidates,
		Audit: &store.ApprovalOutboxEntry{
			ID: uuid.NewString(), EventType: "artifact_event.recorded", PayloadJSON: auditPayload, CreatedAt: now,
		},
	})
	if err != nil {
		if errors.Is(err, store.ErrIdempotencyConflict) {
			return nil, bundleError(connect.CodeAlreadyExists, "artifact_event_conflict",
				fmt.Errorf("event %s/%s already recorded with different content", event.SourceID, event.EventID))
		}
		return nil, internalBundleError("record artifact event", err)
	}
	return connect.NewResponse(&orchestratorv1.RecordArtifactEventResponse{
		EventRecordId: result.Event.ID, Created: result.Created,
		NewCandidates: result.NewCandidates, UpdatedLocations: result.UpdatedLocations,
	}), nil
}

func (s *BundleService) ListBundles(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListBundlesRequest],
) (*connect.Response[orchestratorv1.ListBundlesResponse], error) {
	actor, internal := bundleActor(ctx)
	if !internal && req.Msg.GetReleaseDefinitionId() == "" {
		return nil, bundleError(connect.CodePermissionDenied, "not_authorized", errors.New("release_definition_id is required"))
	}
	if !internal {
		if err := s.authorizeDefinition(ctx, actor, req.Msg.GetReleaseDefinitionId()); err != nil {
			return nil, err
		}
	}
	statuses := make([]store.BundleStatus, 0, len(req.Msg.GetStatusFilter()))
	for _, status := range req.Msg.GetStatusFilter() {
		converted := bundleStatusFromProto(status)
		if !converted.Valid() {
			return nil, bundleError(connect.CodeInvalidArgument, "invalid_status", errors.New("status_filter contains an invalid status"))
		}
		statuses = append(statuses, converted)
	}
	pageSize := 0
	pageToken := ""
	if pagination := req.Msg.GetPagination(); pagination != nil {
		pageSize = int(pagination.GetPageSize())
		pageToken = pagination.GetPageToken()
	}
	page, err := s.store.Bundles().List(ctx, store.BundleListFilter{
		ReleaseDefinitionID: req.Msg.GetReleaseDefinitionId(), Statuses: statuses,
		ChartName: req.Msg.GetChartNameFilter(), PageSize: pageSize, PageToken: pageToken,
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidCursor) {
			return nil, bundleError(connect.CodeInvalidArgument, "invalid_page_token", errors.New("page_token does not match the current filters"))
		}
		return nil, internalBundleError("list bundles", err)
	}
	bundles := make([]*orchestratorv1.BundleSummary, len(page.Bundles))
	for index, bundle := range page.Bundles {
		bundles[index] = bundleSummaryToProto(bundle)
	}
	return connect.NewResponse(&orchestratorv1.ListBundlesResponse{
		Bundles:    bundles,
		Pagination: &commonv1.PaginationResponse{NextPageToken: page.NextPageToken, TotalSize: page.TotalSize},
	}), nil
}

func (s *BundleService) GetBundle(
	ctx context.Context,
	req *connect.Request[orchestratorv1.GetBundleRequest],
) (*connect.Response[orchestratorv1.GetBundleResponse], error) {
	if strings.TrimSpace(req.Msg.GetBundleId()) == "" {
		return nil, bundleError(connect.CodeInvalidArgument, "missing_required_field", errors.New("bundle_id is required"))
	}
	actor, internal := bundleActor(ctx)
	if !internal {
		if req.Msg.GetReleaseDefinitionId() == "" {
			return nil, bundleError(connect.CodePermissionDenied, "not_authorized", errors.New("release_definition_id is required"))
		}
		if err := s.authorizeDefinition(ctx, actor, req.Msg.GetReleaseDefinitionId()); err != nil {
			return nil, err
		}
	}
	bundle, err := s.store.Bundles().Get(ctx, req.Msg.GetBundleId())
	if errors.Is(err, store.ErrNotFound) {
		bundle, err = s.store.Bundles().GetByAlias(ctx, req.Msg.GetBundleId())
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, bundleError(connect.CodeNotFound, "bundle_not_found",
			fmt.Errorf("bundle %s not found", req.Msg.GetBundleId()))
	}
	if err != nil {
		return nil, internalBundleError("get bundle", err)
	}
	showEvidenceRefs := internal || actorHasRole(actor, string(store.RolePlatformAdmin))
	detail := &orchestratorv1.BundleDetail{
		Summary: bundleSummaryToProto(bundle), GitCommit: bundle.GitCommit, PipelineId: bundle.PipelineID,
		SignatureDigest: bundle.SignatureDigest, SbomDigest: bundle.SBOMDigest,
		ProvenanceDigest: bundle.ProvenanceDigest,
	}
	if showEvidenceRefs {
		detail.SignatureRef = bundle.SignatureRef
		detail.SbomRef = bundle.SBOMRef
		detail.ProvenanceRef = bundle.ProvenanceRef
	}
	return connect.NewResponse(&orchestratorv1.GetBundleResponse{Bundle: detail}), nil
}

func (s *BundleService) validateSubmitBundle(msg *orchestratorv1.SubmitBundleRequest) error {
	if strings.TrimSpace(msg.GetName()) == "" {
		return bundleError(connect.CodeInvalidArgument, "missing_required_field", errors.New("name is required"))
	}
	if len([]rune(strings.TrimSpace(msg.GetName()))) > 256 {
		return bundleError(connect.CodeInvalidArgument, "invalid_argument", errors.New("name exceeds 256 characters"))
	}
	if strings.TrimSpace(msg.GetChartRef()) == "" {
		return bundleError(connect.CodeInvalidArgument, "missing_chart_ref", errors.New("chart_ref is required"))
	}
	if strings.TrimSpace(msg.GetChartVersion()) == "" || strings.TrimSpace(msg.GetGitCommit()) == "" || strings.TrimSpace(msg.GetPipelineId()) == "" {
		return bundleError(connect.CodeInvalidArgument, "missing_required_field", errors.New("chart_version, git_commit, and pipeline_id are required"))
	}
	if msg.GetChartDigest() == "" {
		return bundleError(connect.CodeInvalidArgument, "missing_chart_digest", errors.New("chart_digest is required"))
	}
	if !sha256DigestPattern.MatchString(msg.GetChartDigest()) {
		return bundleError(connect.CodeInvalidArgument, "invalid_digest_format", errors.New("chart_digest must be sha256:<64 lowercase hex>"))
	}
	if len(msg.GetImages()) == 0 || len(msg.GetImages()) > 100 {
		return bundleError(connect.CodeInvalidArgument, "too_many_images", fmt.Errorf("images count %d must be between 1 and 100", len(msg.GetImages())))
	}
	if len(msg.GetArtifacts()) > 500 {
		return bundleError(connect.CodeInvalidArgument, "invalid_artifact", fmt.Errorf("artifacts count %d exceeds maximum 500", len(msg.GetArtifacts())))
	}
	if err := s.validateSourceRef(msg.GetChartRef(), true); err != nil {
		return err
	}
	paths := make(map[string]struct{}, len(msg.GetImages()))
	for index, image := range msg.GetImages() {
		if image.GetDigest() == "" {
			return bundleError(connect.CodeInvalidArgument, "missing_image_digest", fmt.Errorf("images[%d].digest is required", index))
		}
		if !sha256DigestPattern.MatchString(image.GetDigest()) {
			return bundleError(connect.CodeInvalidArgument, "invalid_digest_format", fmt.Errorf("images[%d].digest must be sha256:<64 lowercase hex>", index))
		}
		if !valuesPathPattern.MatchString(image.GetValuesPath()) || len([]byte(image.GetValuesPath())) > 256 {
			return bundleError(connect.CodeInvalidArgument, "invalid_values_path", fmt.Errorf("images[%d].values_path %q is invalid", index, image.GetValuesPath()))
		}
		if _, exists := paths[image.GetValuesPath()]; exists {
			return bundleError(connect.CodeInvalidArgument, "duplicate_values_path", fmt.Errorf("images[%d]: duplicate values_path %q", index, image.GetValuesPath()))
		}
		paths[image.GetValuesPath()] = struct{}{}
		if image.GetValueKind() == commonv1.ImageValueKind_IMAGE_VALUE_KIND_UNSPECIFIED {
			return bundleError(connect.CodeInvalidArgument, "invalid_value_kind", fmt.Errorf("images[%d].value_kind must not be UNSPECIFIED", index))
		}
		if err := s.validateSourceRef(image.GetRef(), false); err != nil {
			return err
		}
	}
	for _, evidence := range []struct {
		name string
		ref  *commonv1.ArtifactReference
	}{{"signature", msg.GetSignature()}, {"sbom", msg.GetSbom()}, {"provenance", msg.GetProvenance()}} {
		if evidence.ref == nil {
			continue
		}
		if strings.TrimSpace(evidence.ref.GetRef()) == "" || !sha256DigestPattern.MatchString(evidence.ref.GetDigest()) {
			return bundleError(connect.CodeInvalidArgument, "invalid_artifact", fmt.Errorf("%s ref and digest are required", evidence.name))
		}
	}
	for index, artifact := range msg.GetArtifacts() {
		if artifact.GetArtifactType() == commonv1.ArtifactType_ARTIFACT_TYPE_UNSPECIFIED ||
			strings.TrimSpace(artifact.GetRef()) == "" || !sha256DigestPattern.MatchString(artifact.GetDigest()) {
			return bundleError(connect.CodeInvalidArgument, "invalid_artifact", fmt.Errorf("artifacts[%d] is invalid", index))
		}
	}
	return nil
}

func (s *BundleService) validateSourceRef(raw string, chart bool) error {
	candidate := raw
	if chart {
		if !strings.HasPrefix(candidate, "oci://") {
			return bundleError(connect.CodeInvalidArgument, "missing_chart_ref", errors.New("chart_ref must use oci://"))
		}
	} else {
		candidate = "oci://" + strings.TrimPrefix(candidate, "oci://")
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return bundleError(connect.CodeInvalidArgument, "invalid_argument", errors.New("artifact ref is invalid"))
	}
	if len(s.sourceRegistries) == 0 {
		return nil
	}
	plain := strings.TrimPrefix(candidate, "oci://")
	for _, registry := range s.sourceRegistries {
		if strings.HasPrefix(plain, strings.TrimPrefix(registry.Prefix, "oci://")) {
			return nil
		}
	}
	return bundleError(connect.CodeInvalidArgument, "source_not_allowed", fmt.Errorf("source %q is not in the allowlist", parsed.Host))
}

func bundleFromProto(msg *orchestratorv1.SubmitBundleRequest) (*store.ReleaseBundle, []*store.CandidateArtifact, error) {
	bundle := &store.ReleaseBundle{
		Name: strings.TrimSpace(msg.GetName()), ChartRef: msg.GetChartRef(), ChartVersion: strings.TrimSpace(msg.GetChartVersion()),
		ChartDigest: msg.GetChartDigest(), GitCommit: strings.TrimSpace(msg.GetGitCommit()), PipelineID: strings.TrimSpace(msg.GetPipelineId()),
	}
	bundle.Images = make([]store.BundleImage, len(msg.GetImages()))
	for index, image := range msg.GetImages() {
		bundle.Images[index] = store.BundleImage{
			Ref: image.GetRef(), Digest: image.GetDigest(), ValuesPath: image.GetValuesPath(),
			ValueKind: imageValueKindFromProto(image.GetValueKind()),
		}
	}
	if evidence := msg.GetSignature(); evidence != nil {
		bundle.SignatureRef, bundle.SignatureDigest = evidence.GetRef(), evidence.GetDigest()
	}
	if evidence := msg.GetSbom(); evidence != nil {
		bundle.SBOMRef, bundle.SBOMDigest = evidence.GetRef(), evidence.GetDigest()
	}
	if evidence := msg.GetProvenance(); evidence != nil {
		bundle.ProvenanceRef, bundle.ProvenanceDigest = evidence.GetRef(), evidence.GetDigest()
	}
	return bundle, deriveCandidates(msg), nil
}

func deriveCandidates(msg *orchestratorv1.SubmitBundleRequest) []*store.CandidateArtifact {
	type key struct {
		digest       string
		artifactType store.ArtifactType
	}
	candidates := make(map[key]*store.CandidateArtifact)
	add := func(artifactType store.ArtifactType, ref, digest string, prefer bool) {
		if digest == "" {
			return
		}
		identity := key{digest: digest, artifactType: artifactType}
		if existing, ok := candidates[identity]; ok {
			if prefer && ref != "" {
				existing.Ref = ref
			}
			return
		}
		candidates[identity] = &store.CandidateArtifact{ArtifactType: artifactType, Ref: ref, Digest: digest}
	}
	add(store.ArtifactChart, msg.GetChartRef(), msg.GetChartDigest(), false)
	for _, image := range msg.GetImages() {
		add(store.ArtifactImage, image.GetRef(), image.GetDigest(), false)
	}
	if evidence := msg.GetSignature(); evidence != nil {
		add(store.ArtifactSignature, evidence.GetRef(), evidence.GetDigest(), false)
	}
	if evidence := msg.GetSbom(); evidence != nil {
		add(store.ArtifactSBOM, evidence.GetRef(), evidence.GetDigest(), false)
	}
	if evidence := msg.GetProvenance(); evidence != nil {
		add(store.ArtifactProvenance, evidence.GetRef(), evidence.GetDigest(), false)
	}
	for _, artifact := range msg.GetArtifacts() {
		add(artifactTypeFromCommonProto(artifact.GetArtifactType()), artifact.GetRef(), artifact.GetDigest(), true)
	}
	result := make([]*store.CandidateArtifact, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ArtifactType != result[j].ArtifactType {
			return result[i].ArtifactType < result[j].ArtifactType
		}
		return result[i].Digest < result[j].Digest
	})
	return result
}

func canonicalBundleDigest(msg *orchestratorv1.SubmitBundleRequest) string {
	lines := []string{
		strings.TrimSpace(msg.GetName()), msg.GetChartRef(), strings.TrimSpace(msg.GetChartVersion()), msg.GetChartDigest(),
		strings.TrimSpace(msg.GetGitCommit()), strings.TrimSpace(msg.GetPipelineId()),
		evidenceLine(msg.GetSignature()), evidenceLine(msg.GetSbom()), evidenceLine(msg.GetProvenance()),
	}
	imageLines := make([]string, len(msg.GetImages()))
	for index, image := range msg.GetImages() {
		imageLines[index] = strings.Join([]string{
			image.GetRef(), image.GetDigest(), image.GetValuesPath(), image.GetValueKind().String(),
		}, "|")
	}
	sort.Strings(lines)
	sort.Strings(imageLines)
	payload := "release-bundle/v1\n" + strings.Join(append(lines, imageLines...), "\n")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func evidenceLine(reference *commonv1.ArtifactReference) string {
	if reference == nil {
		return ""
	}
	return reference.GetRef() + "|" + reference.GetDigest()
}

func bundleSummaryToProto(bundle *store.ReleaseBundle) *orchestratorv1.BundleSummary {
	images := make([]*orchestratorv1.BundleImageBindingSummary, len(bundle.Images))
	for index, image := range bundle.Images {
		images[index] = &orchestratorv1.BundleImageBindingSummary{
			Ref: image.Ref, Digest: image.Digest, ValuesPath: image.ValuesPath,
			ValueKind: imageValueKindToProto(image.ValueKind),
		}
	}
	return &orchestratorv1.BundleSummary{
		Id: bundle.ID, Name: bundle.Name,
		Digest: &commonv1.ReleaseDigest{Algorithm: bundle.DigestAlg, Value: bundle.DigestValue},
		Status: bundleStatusToProto(bundle.Status), ChartRef: bundle.ChartRef,
		ChartVersion: bundle.ChartVersion, ChartDigest: bundle.ChartDigest,
		Images: images, CreatedAt: timestamppb.New(bundle.CreatedAt),
	}
}

func (s *BundleService) authorizeDefinition(ctx context.Context, actor authctx.Actor, definitionID string) error {
	definition, err := s.store.Definitions().Get(ctx, definitionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return bundleError(connect.CodePermissionDenied, "not_authorized", errors.New("missing required role or organization binding"))
		}
		return internalBundleError("authorize release definition", err)
	}
	if definition.OwnerOrganizationID != nil && *definition.OwnerOrganizationID != actor.OrganizationID {
		return bundleError(connect.CodePermissionDenied, "not_authorized", errors.New("missing required role or organization binding"))
	}
	if err := s.store.Bindings().RequireActive(ctx, actor.OrganizationID, definition.CustomerID); err != nil {
		return bundleError(connect.CodePermissionDenied, "not_authorized", errors.New("missing required role or organization binding"))
	}
	return nil
}

func bundleActor(ctx context.Context) (authctx.Actor, bool) {
	actor, ok := authctx.ActorFromContext(ctx)
	return actor, ok && actor.Service != ""
}

func actorHasRole(actor authctx.Actor, role string) bool {
	for _, candidate := range actor.Roles {
		if candidate == role {
			return true
		}
	}
	return false
}

func bundleStatusFromProto(status commonv1.BundleStatus) store.BundleStatus {
	switch status {
	case commonv1.BundleStatus_BUNDLE_STATUS_RECEIVED:
		return store.BundleReceived
	case commonv1.BundleStatus_BUNDLE_STATUS_VALIDATED:
		return store.BundleValidated
	case commonv1.BundleStatus_BUNDLE_STATUS_REJECTED:
		return store.BundleRejected
	case commonv1.BundleStatus_BUNDLE_STATUS_ARCHIVED:
		return store.BundleArchived
	default:
		return ""
	}
}

func bundleStatusToProto(status store.BundleStatus) commonv1.BundleStatus {
	switch status {
	case store.BundleReceived:
		return commonv1.BundleStatus_BUNDLE_STATUS_RECEIVED
	case store.BundleValidated:
		return commonv1.BundleStatus_BUNDLE_STATUS_VALIDATED
	case store.BundleRejected:
		return commonv1.BundleStatus_BUNDLE_STATUS_REJECTED
	case store.BundleArchived:
		return commonv1.BundleStatus_BUNDLE_STATUS_ARCHIVED
	default:
		return commonv1.BundleStatus_BUNDLE_STATUS_UNSPECIFIED
	}
}

func imageValueKindFromProto(kind commonv1.ImageValueKind) store.ImageValueKind {
	switch kind {
	case commonv1.ImageValueKind_IMAGE_VALUE_KIND_FULL_REFERENCE:
		return store.ImageValueFullReference
	case commonv1.ImageValueKind_IMAGE_VALUE_KIND_REPOSITORY:
		return store.ImageValueRepository
	case commonv1.ImageValueKind_IMAGE_VALUE_KIND_TAG:
		return store.ImageValueTag
	case commonv1.ImageValueKind_IMAGE_VALUE_KIND_DIGEST:
		return store.ImageValueDigest
	default:
		return ""
	}
}

func imageValueKindToProto(kind store.ImageValueKind) commonv1.ImageValueKind {
	switch kind {
	case store.ImageValueFullReference:
		return commonv1.ImageValueKind_IMAGE_VALUE_KIND_FULL_REFERENCE
	case store.ImageValueRepository:
		return commonv1.ImageValueKind_IMAGE_VALUE_KIND_REPOSITORY
	case store.ImageValueTag:
		return commonv1.ImageValueKind_IMAGE_VALUE_KIND_TAG
	case store.ImageValueDigest:
		return commonv1.ImageValueKind_IMAGE_VALUE_KIND_DIGEST
	default:
		return commonv1.ImageValueKind_IMAGE_VALUE_KIND_UNSPECIFIED
	}
}

func artifactTypeFromCommonProto(artifactType commonv1.ArtifactType) store.ArtifactType {
	switch artifactType {
	case commonv1.ArtifactType_ARTIFACT_TYPE_IMAGE:
		return store.ArtifactImage
	case commonv1.ArtifactType_ARTIFACT_TYPE_CHART:
		return store.ArtifactChart
	case commonv1.ArtifactType_ARTIFACT_TYPE_SBOM:
		return store.ArtifactSBOM
	case commonv1.ArtifactType_ARTIFACT_TYPE_PROVENANCE:
		return store.ArtifactProvenance
	case commonv1.ArtifactType_ARTIFACT_TYPE_SIGNATURE:
		return store.ArtifactSignature
	default:
		return ""
	}
}

func bundleError(code connect.Code, reason string, cause error) error {
	return connect.NewError(code, fmt.Errorf("%s: %w", reason, cause))
}

func internalBundleError(operation string, cause error) error {
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s: internal error: %v", operation, cause))
}

var _ orchestratorv1connect.BundleServiceHandler = (*BundleService)(nil)
