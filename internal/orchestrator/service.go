// Package orchestrator implements the release orchestration Connect service.
package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/orchestrator/operation"
	"github.com/ndzuki/release-manager/internal/orchestrator/preflight"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/trust"
	"github.com/ndzuki/release-manager/internal/vulnerability"
)

// Service implements the OrchestratorServiceHandler Connect interface.
type Service struct {
	store           store.Store
	createOperation OperationCreationUnitOfWork
	verifier        trust.Verifier
	targetEnv       string
	coordinator     *preflight.Coordinator
	vulnEval        *vulnerability.Evaluator
	auditEmitter    audit.Sink
	logger          *slog.Logger
}

func NewService(
	st store.Store,
	createOperation OperationCreationUnitOfWork,
	verifier trust.Verifier,
	targetEnv string,
	auditEmitter audit.Sink,
	logger *slog.Logger,
) *Service {
	return &Service{
		store:           st,
		createOperation: createOperation,
		verifier:        verifier,
		targetEnv:       targetEnv,
		coordinator:     preflight.NewCoordinator(st.Outbox(), st.Operations(), st.Operators(), st.Definitions(), st.PreflightLifecycles(), logger),
		auditEmitter:    auditEmitter,
		logger:          logger,
	}
}

// CreateOperation creates a new release operation from the given request.
//
//nolint:gocyclo // operation creation validates multiple independent policy gates
func (s *Service) CreateOperation(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateOperationRequest],
) (*connect.Response[orchestratorv1.CreateOperationResponse], error) {
	msg := req.Msg
	opType := store.OperationType(msg.GetOperationType())
	if opType != store.OperationInstall && opType != store.OperationUpgrade {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("invalid_operation_type: only INSTALL and UPGRADE are accepted"))
	}
	if msg.GetIdempotencyKey() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("idempotency_key is required"))
	}

	def, err := s.store.Definitions().Get(ctx, msg.GetReleaseDefinitionId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("definition_not_found: %s", msg.GetReleaseDefinitionId()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("definition lookup: %w", err))
	}
	if err := checkDefinitionOperable(def); err != nil {
		return nil, err
	}
	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("customer_disabled: %w", err))
	}
	if msg.GetActor().GetOrganization() == "" || msg.GetActor().GetUserId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("actor organization and user_id are required"))
	}
	if err := s.authorizeOperationActor(ctx, msg.GetActor().GetOrganization(), msg.GetActor().GetUserId(), def.CustomerID); err != nil {
		return nil, err
	}

	existing, err := s.findIdempotentOperation(ctx, msg)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		s.logger.Info("idempotent operation found", "key", msg.GetIdempotencyKey(), "op_id", existing.ID)
		return connect.NewResponse(s.toResponse(existing)), nil
	}

	active, err := s.store.Operations().HasActiveForDefinition(ctx, msg.GetReleaseDefinitionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("active check: %w", err))
	}
	if active {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("release_busy: definition %s has active operation", msg.GetReleaseDefinitionId()))
	}
	activeEmergency, err := s.store.Operations().HasActiveEmergencyForDefinition(ctx, msg.GetReleaseDefinitionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("emergency check: %w", err))
	}
	if activeEmergency {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("release_busy: running EMERGENCY operation"))
	}

	bundle, err := s.store.Bundles().Get(ctx, msg.GetBundleId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("bundle_not_found: %s", msg.GetBundleId()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("bundle lookup: %w", err))
	}
	switch bundle.Status {
	case store.BundleReceived:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_not_ready"))
	case store.BundleRejected:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_rejected"))
	case store.BundleValidated:
	case store.BundleArchived:
		switch bundle.ArchivedFromStatus {
		case store.BundleValidated:
		case store.BundleRejected:
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_rejected"))
		default:
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_not_ready"))
		}
	default:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_not_ready"))
	}
	if !chartNameMatches(bundle.ChartRef, def.ChartName) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("chart_mismatch: bundle chart_ref %q does not match definition chart_name %q", bundle.ChartRef, def.ChartName))
	}

	var verifyResult commonv1.VerificationResult
	if msg.SignatureRef != nil && s.verifier != nil {
		policy := trust.DefaultPolicy(s.targetEnv)
		digest := bundle.ChartDigest
		if digest == "" {
			digest = fmt.Sprintf("%x", sha256.Sum256([]byte(msg.GetBundleId()+"|"+def.ID)))
		}
		out, err := s.verifier.Verify(ctx, trust.Input{Digest: digest, SignatureRef: msg.SignatureRef, Policy: policy})
		if err != nil {
			if policy.FailClosed {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("verification_unavailable: %w", err))
			}
			verifyResult = commonv1.VerificationResult_VERIFICATION_RESULT_VERIFICATION_UNAVAILABLE
		} else {
			verifyResult = trust.StatusToProto(out.Status)
			if out.Status == store.VerificationRejected {
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("artifact trust rejected: %s", out.Summary))
			}
		}
	}

	revision, err := s.checkValuesRevision(ctx, def, msg.GetValuesRevisionId())
	if err != nil {
		return nil, err
	}
	if err := s.checkReleaseState(ctx, def, opType, int(msg.GetExpectedCurrentRevision())); err != nil {
		return nil, err
	}
	merged, err := prepareValues(revision, msg.GetValuesPatch())
	if err != nil {
		if errors.Is(err, errSecretLiteralForbidden) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("secret_literal_forbidden"))
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	now := time.Now().UTC()
	policyVersion := trust.DefaultPolicy(s.targetEnv).PolicyVersion
	imageRefsJSON, imageDigestsJSON, err := bundleImageDigests(bundle.Images)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal bundle image digests: %w", err))
	}
	op := &store.Operation{
		ID: uuid.New().String(), OperationType: opType, Status: operation.InitialStatus(),
		ReleaseDefinitionID: msg.GetReleaseDefinitionId(), IdempotencyKey: msg.GetIdempotencyKey(),
		IdempotencyScope: idempotencyScope(msg.GetActor().GetOrganization(), msg.GetReleaseDefinitionId()),
		RequestHash:      hashRequest(msg, string(merged.patch)), BundleID: msg.GetBundleId(),
		BundleChartRef: bundle.ChartRef, BundleChartDigest: bundle.ChartDigest,
		ImageRefsJSON: imageRefsJSON, ImageDigestsJSON: imageDigestsJSON, PolicyVersion: policyVersion,
		ValuesRevisionID: msg.GetValuesRevisionId(), ExpectedRevision: int(msg.GetExpectedCurrentRevision()),
		ValuesPatch: merged.patch, PatchDigest: merged.patchDigest, EffectiveValuesDigest: merged.effectiveDigest,
		Actor:     store.ActorContext{UserID: msg.GetActor().GetUserId(), Organization: msg.GetActor().GetOrganization()},
		CreatedAt: now, UpdatedAt: now,
	}
	dispatch, dispatchErr := s.coordinator.Dispatch(ctx, op, bundleToProto(bundle), merged.effective)
	if dispatchErr != nil {
		payload, marshalErr := (&preflight.CommandPayload{
			Stage: preflight.StageArtifact, OperationID: op.ID, BundleID: op.BundleID, DefinitionID: def.ID,
			Bundle: bundleToProto(bundle), Namespace: def.Namespace, ReleaseName: def.ReleaseName, Values: merged.effective,
			ValuesRevisionID: op.ValuesRevisionID, ValuesPatch: op.ValuesPatch,
			ExpectedCurrentRevision: op.ExpectedRevision, TargetRevision: op.TargetRevision,
		}).Marshal()
		if marshalErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal deferred dispatch: %w", marshalErr))
		}
		dispatch = &store.OutboxEntry{
			ID: uuid.New().String(), CommandID: fmt.Sprintf("%s:artifact", op.ID),
			OperationID: op.ID, OperationType: string(op.OperationType), Payload: payload,
		}
	}
	if s.createOperation == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("operation creation unit of work is not configured"))
	}
	artifactDigests := make([]string, 0, len(bundle.Images)+1)
	if bundle.ChartDigest != "" {
		artifactDigests = append(artifactDigests, bundle.ChartDigest)
	}
	for _, image := range bundle.Images {
		if image.Digest != "" {
			artifactDigests = append(artifactDigests, image.Digest)
		}
	}
	if _, err := s.createOperation(ctx, CreateOperationRequest{
		Operation: op, Dispatch: dispatch, CandidateArtifactDigests: artifactDigests,
	}); err != nil {
		switch {
		case errors.Is(err, store.ErrReleaseBusy):
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("release_busy"))
		case errors.Is(err, store.ErrDuplicateKey):
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("idempotency_conflict"))
		case errors.Is(err, store.ErrBundleNotReady):
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_not_ready"))
		case errors.Is(err, store.ErrBundleRejected):
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle_rejected"))
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create operation: %w", err))
		}
	}
	if dispatchErr != nil {
		s.logger.Warn("preflight dispatch deferred", "op_id", op.ID, "err", dispatchErr)
	}

	return connect.NewResponse(&orchestratorv1.CreateOperationResponse{
		OperationId: op.ID, State: string(op.Status), PreflightId: op.ID,
		AcceptedAt: timestamppb.New(op.CreatedAt), VerificationResult: verifyResult,
	}), nil
}

func (s *Service) findIdempotentOperation(ctx context.Context, msg *orchestratorv1.CreateOperationRequest) (*store.Operation, error) {
	scope := idempotencyScope(msg.GetActor().GetOrganization(), msg.GetReleaseDefinitionId())
	existing, err := s.store.Operations().GetByIdempotencyScopeAndKey(ctx, scope, msg.GetIdempotencyKey())
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("idempotency lookup: %w", err))
	}
	if existing.RequestHash != hashRequest(msg, canonicalPatchForHash(msg.GetValuesPatch())) {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("idempotency_conflict"))
	}
	return existing, nil
}

// PublishRelease triggers the release pipeline for a definition (skeleton).
func (s *Service) PublishRelease(
	ctx context.Context,
	req *connect.Request[orchestratorv1.PublishReleaseRequest],
) (*connect.Response[orchestratorv1.PublishReleaseResponse], error) {
	msg := req.Msg

	// Verify the definition exists and both customer and cluster are active.
	def, err := s.store.Definitions().Get(ctx, msg.ReleaseDefinitionId)
	if err == store.ErrNotFound {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("release_definition not found: %s", msg.ReleaseDefinitionId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("definition lookup: %w", err))
	}
	if err := checkDefinitionOperable(def); err != nil {
		return nil, err
	}

	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}

	// AC-014-04: disabled cluster cannot be a release target.
	cluster, err := s.store.Clusters().Get(ctx, def.ClusterID)
	if err == store.ErrNotFound {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("cluster %q not found for definition %s", def.ClusterID, msg.ReleaseDefinitionId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cluster lookup: %w", err))
	}
	if cluster.Status == store.ClusterDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("cluster %q is disabled, cannot publish", cluster.ID))
	}

	s.logger.Info("publish release requested (skeleton)", "definition", msg.ReleaseDefinitionId)
	return connect.NewResponse(&orchestratorv1.PublishReleaseResponse{
		OperationId: "",
		Status:      "not_implemented",
	}), nil
}

func (s *Service) toResponse(op *store.Operation) *orchestratorv1.CreateOperationResponse {
	return &orchestratorv1.CreateOperationResponse{
		OperationId: op.ID,
		State:       string(op.Status),
		PreflightId: op.ID, // preflight_id = operation_id for initial phase
		AcceptedAt:  timestamppb.New(op.CreatedAt),
	}
}

// bundleImageDigests converts bundle images to JSON byte arrays for storage.
func bundleImageDigests(images []store.BundleImage) (refsJSON, digestsJSON []byte, err error) {
	if len(images) == 0 {
		return nil, nil, nil
	}
	refs := make([]string, len(images))
	digests := make([]string, len(images))
	for i, img := range images {
		refs[i] = img.Ref
		digests[i] = img.Digest
	}
	refsJSON, err = json.Marshal(refs)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal image refs: %w", err)
	}
	digestsJSON, err = json.Marshal(digests)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal image digests: %w", err)
	}
	return refsJSON, digestsJSON, nil
}

func bundleToProto(bundle *store.ReleaseBundle) *commonv1.ReleaseBundle {
	images := make([]*commonv1.BundleImage, 0, len(bundle.Images))
	for _, image := range bundle.Images {
		images = append(images, &commonv1.BundleImage{
			Ref: image.Ref, Digest: image.Digest, ValuesPath: image.ValuesPath,
		})
	}
	return &commonv1.ReleaseBundle{
		Id: bundle.ID, Name: bundle.Name,
		Digest:   &commonv1.ReleaseDigest{Algorithm: bundle.DigestAlg, Value: bundle.DigestValue},
		Status:   commonv1.BundleStatus(commonv1.BundleStatus_value["BUNDLE_STATUS_"+strings.ToUpper(string(bundle.Status))]),
		ChartRef: bundle.ChartRef, ChartVersion: bundle.ChartVersion, ChartDigest: bundle.ChartDigest,
		Images: images, GitCommit: bundle.GitCommit, PipelineId: bundle.PipelineID,
		SignatureRef: bundle.SignatureRef, SbomRef: bundle.SBOMRef, ProvenanceRef: bundle.ProvenanceRef,
		CreatedAt: timestamppb.New(bundle.CreatedAt),
	}
}

// hashRequest computes a deterministic hash of the canonical request payload.
func hashRequest(req *orchestratorv1.CreateOperationRequest, canonicalPatch string) string {
	return hashOperationRequest(store.OperationType(req.GetOperationType()), req.GetBundleId(), req.GetReleaseDefinitionId(),
		req.GetValuesRevisionId(), canonicalPatch, int(req.GetExpectedCurrentRevision()), 0, "")
}

func hashOperationRequest(opType store.OperationType, bundleID, definitionID, valuesRevisionID, canonicalPatch string, expectedRevision, targetRevision int, reason string) string {
	payload := fmt.Sprintf(`{"operation_type":%q,"bundle_id":%q,"release_definition_id":%q,"values_revision_id":%q,"values_patch":%s,"expected_current_revision":%d,"target_revision":%d,"reason":%q}`,
		string(opType), bundleID, definitionID, valuesRevisionID, canonicalPatch, expectedRevision, targetRevision, reason)
	h := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", h)
}

func (s *Service) authorizeOperationActor(ctx context.Context, orgID, userID, customerID string) error {
	if err := s.store.Bindings().RequireActive(ctx, orgID, customerID); err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrBindingRevoked) {
			return connect.NewError(connect.CodePermissionDenied, errors.New("permission_denied"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("binding check: %w", err))
	}
	member, err := s.store.OrgMembers().Get(ctx, orgID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodePermissionDenied, errors.New("permission_denied"))
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("organization member lookup: %w", err))
	}
	if member.Role != store.RoleReleaseAdmin && member.Role != store.RolePlatformAdmin {
		return connect.NewError(connect.CodePermissionDenied, errors.New("permission_denied"))
	}
	return nil
}

func (s *Service) rollbackRequestHash(msg *orchestratorv1.RollbackReleaseRequest) string {
	return hashOperationRequest(store.OperationRollback, "", msg.GetReleaseDefinitionId(), "", "{}",
		int(msg.GetExpectedCurrentRevision()), int(msg.GetTargetRevision()), msg.GetReason())
}

func canonicalPatchForHash(raw string) string {
	merged, err := prepareValues(&store.ValuesRevision{Values: []byte(`{}`)}, raw)
	if err != nil {
		return `{}`
	}
	return string(merged.patch)
}

func idempotencyScope(orgID, definitionID string) string {
	return orgID + ":" + definitionID
}

func (s *Service) checkValuesRevision(ctx context.Context, def *store.ReleaseDefinition, revisionID string) (*store.ValuesRevision, error) {
	if revisionID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("revision_not_approved"))
	}
	revision, err := s.store.Values().Get(ctx, revisionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("revision_mismatch: values_revision not found: %s", revisionID))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("values revision lookup: %w", err))
	}
	if revision.ReleaseDefinitionID != def.ID {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("values_revision %s belongs to release_definition %s", revision.ID, revision.ReleaseDefinitionID))
	}
	if revision.Status != store.ValuesStatusApproved {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("revision_not_approved: values_revision %s must be approved", revision.ID))
	}
	return revision, nil
}

// checkCustomerNotDisabled verifies the customer is not disabled.
// Returns PermissionDenied if the customer is disabled.
func (s *Service) checkCustomerNotDisabled(ctx context.Context, customerID string) error {
	cust, err := s.store.Customers().Get(ctx, customerID)
	if err != nil {
		return fmt.Errorf("customer lookup: %w", err)
	}
	if cust.Status == store.CustomerDisabled {
		return fmt.Errorf("customer %s is disabled", customerID)
	}
	return nil
}

func (s *Service) checkReleaseState(
	ctx context.Context,
	def *store.ReleaseDefinition,
	opType store.OperationType,
	expected int,
) error {
	installed, err := s.store.Inventories().GetByDefinition(ctx, def.ID)
	if opType == store.OperationInstall {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("inventory lookup: %w", err))
		}
		if installed.InventoryStatus == store.InventoryActive {
			return connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("release_already_exists: installed release exists for definition %s", def.ID))
		}
		return nil
	}

	if expected < 1 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("expected_current_revision must be >= 1"))
	}
	if errors.Is(err, store.ErrNotFound) || (err == nil && installed.InventoryStatus != store.InventoryActive) {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("release_not_found: no installed release for definition %s", def.ID))
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("inventory lookup: %w", err))
	}
	if installed.Revision != expected {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("revision_conflict: expected revision %d, but current revision is %d", expected, installed.Revision))
	}
	return nil
}

// chartNameMatches performs a loose match between a chart reference and a chart name.
// Registry host prefixes (e.g., "registry.example.com/") are stripped from both
// before comparison (AC-067-03).
func chartNameMatches(chartRef, chartName string) bool {
	ref := extractChartName(chartRef)
	name := extractChartName(chartName)
	return ref == name
}

// extractChartName strips the registry host prefix from a chart reference.
// "registry.example.com/nginx" → "nginx"
// "nginx" → "nginx"
func extractChartName(ref string) string {
	if idx := strings.LastIndex(ref, "/"); idx >= 0 {
		return ref[idx+1:]
	}
	return ref
}

// Compile-time check: Service implements the Connect handler interface.
var _ orchestratorv1connect.OrchestratorServiceHandler = (*Service)(nil)

// checkDefinitionOperable returns a stable release_definition_disabled error
// if the definition is not in active state. Uses FailedPrecondition for both
// draft and disabled; callers that want to differentiate can check the status
// before calling this function.
func checkDefinitionOperable(def *store.ReleaseDefinition) error {
	if def.Status == store.DefStatusActive {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("release_definition_disabled: definition %s is %s", def.ID, def.Status))
}

// emitAudit emits an audit event through the configured sink, if any.
func (s *Service) emitAudit(ev *store.AuditEvent) {
	if s.auditEmitter == nil {
		return
	}
	result := s.auditEmitter.Emit(ev)
	if !result.Accepted {
		s.logger.Warn("audit event rejected",
			"code", string(result.Code),
			"resource_type", ev.ResourceType,
			"action", ev.Action,
		)
	}
}

// auditActor converts an ActorContext to audit actor kind and ID.
func auditActor(actor *store.ActorContext) (kind store.AuditActorKind, actorID string) {
	if actor == nil || actor.UserID == "" {
		return store.AuditActorSystem, "system"
	}
	return store.AuditActorUser, actor.UserID
}
