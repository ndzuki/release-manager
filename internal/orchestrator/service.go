// Package orchestrator implements the release orchestration Connect service.
package orchestrator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"github.com/ndzuki/release-manager/internal/orchestrator/operation"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/trust"
)

// Service implements the OrchestratorServiceHandler Connect interface.
type Service struct {
	store     store.Store
	verifier  trust.Verifier
	targetEnv string
	logger    *slog.Logger
}

// NewService creates a new orchestrator Connect service.
func NewService(st store.Store, verifier trust.Verifier, targetEnv string, logger *slog.Logger) *Service {
	return &Service{store: st, verifier: verifier, targetEnv: targetEnv, logger: logger}
}

// CreateOperation creates a new release operation from the given request.
// Implements REQ-067 validation rules and REQ-023 idempotency.
func (s *Service) CreateOperation(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateOperationRequest],
) (*connect.Response[orchestratorv1.CreateOperationResponse], error) {
	msg := req.Msg

	// 1. Idempotency check (REQ-023 AC-023-02)
	if msg.IdempotencyKey != "" {
		existing, err := s.store.Operations().GetByIdempotencyKey(ctx, msg.IdempotencyKey)
		if err == nil {
			s.logger.Info("idempotent operation found", "key", msg.IdempotencyKey, "op_id", existing.ID)
			return connect.NewResponse(s.toResponse(existing)), nil
		}
		if err != store.ErrNotFound {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("idempotency lookup: %w", err))
		}
		// not found -> proceed
	}

	// 2. Validate operation type
	opType := store.OperationType(msg.OperationType)
	if !opType.Valid() {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("invalid operation_type: %s", msg.OperationType))
	}

	// 3. Lookup release definition
	def, err := s.store.Definitions().Get(ctx, msg.ReleaseDefinitionId)
	if err == store.ErrNotFound {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("release_definition not found: %s", msg.ReleaseDefinitionId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("definition lookup: %w", err))
	}

	// Validate definition status
	if def.Status != store.DefStatusActive {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("release_definition %s is %s", def.ID, def.Status))
	}

	// AC-032-06: Reject standard operations when a running EMERGENCY exists for
	// the same definition.
	if opType.IsStandard() {
		activeEmergency, err := s.store.Operations().HasActiveEmergencyForDefinition(ctx, msg.ReleaseDefinitionId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("emergency active check: %w", err))
		}
		if activeEmergency {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("definition %s has a running EMERGENCY operation; standard operations are denied", msg.ReleaseDefinitionId))
		}
	}

	// 4. Release busy check (REQ-023 AC-023-03, AC-023-06, AC-023-07)
	active, err := s.store.Operations().HasActiveForDefinition(ctx, msg.ReleaseDefinitionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("active check: %w", err))
	}
	if active {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("release_busy: definition %s has active operation", msg.ReleaseDefinitionId))
	}

	// 4.5. Trust verification (REQ-012)
	var verifyResult commonv1.VerificationResult
	if msg.SignatureRef != nil && s.verifier != nil {
		policy := trust.DefaultPolicy(s.targetEnv)
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(msg.BundleId+"|"+def.ID)))

		out, err := s.verifier.Verify(ctx, trust.Input{
			Digest:       digest,
			SignatureRef: msg.SignatureRef,
			Policy:       policy,
		})
		if err != nil {
			if policy.FailClosed {
				return nil, connect.NewError(connect.CodeUnavailable,
					fmt.Errorf("verification_unavailable: %w", err))
			}
			s.logger.Warn("verification backend unavailable, policy_warning", "err", err)
			verifyResult = commonv1.VerificationResult_VERIFICATION_RESULT_VERIFICATION_UNAVAILABLE
		} else {
			verifyResult = trust.StatusToProto(out.Status)
			if out.Status == store.VerificationRejected {
				return nil, connect.NewError(connect.CodeFailedPrecondition,
					fmt.Errorf("artifact trust rejected: %s", out.Summary))
			}
		}
	}

	// 5. Build operation request hash for idempotency
	reqHash := hashRequest(msg)

	// 6. Build domain Operation
	now := time.Now().UTC()
	op := &store.Operation{
		ID:                  uuid.New().String(),
		OperationType:       opType,
		Status:              operation.InitialStatus(),
		ReleaseDefinitionID: msg.ReleaseDefinitionId,
		IdempotencyKey:      msg.IdempotencyKey,
		RequestHash:         reqHash,
		BundleID:            msg.BundleId,
		ValuesRevisionID:    msg.ValuesRevisionId,
		ExpectedRevision:    int(msg.ExpectedCurrentRevision),
		ValuesPatch:         []byte(msg.ValuesPatch),
		Actor: store.ActorContext{
			UserID:       msg.Actor.GetUserId(),
			Organization: msg.Actor.GetOrganization(),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// 7. Persist
	if err := s.store.Operations().Create(ctx, op); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create operation: %w", err))
	}

	// 8. Trigger preflight transition (async in production; synchronous for now)
	//    Standard ops go pending->preflight, EMERGENCY goes pending->queued
	if opType.IsStandard() {
		next, err := operation.Transition(op.Status, operation.EventStartPreflight)
		if err != nil {
			s.logger.Error("preflight transition failed", "op_id", op.ID, "err", err)
		} else {
			_, err = s.store.Operations().UpdateStatus(ctx, op.ID, next, op.StateVersion, "")
			if err != nil {
				s.logger.Error("preflight status update failed", "op_id", op.ID, "err", err)
			} else {
				op.Status = next
				op.StateVersion++
			}
		}
	}

	s.logger.Info("operation created",
		"op_id", op.ID,
		"type", op.OperationType,
		"definition", op.ReleaseDefinitionID,
	)

	return connect.NewResponse(&orchestratorv1.CreateOperationResponse{
		OperationId:        op.ID,
		State:              string(op.Status),
		PreflightId:        op.ID,
		AcceptedAt:         timestamppb.New(op.CreatedAt),
		VerificationResult: verifyResult,
	}), nil
}

// PublishRelease triggers the release pipeline for a definition (skeleton).
func (s *Service) PublishRelease(
	ctx context.Context,
	req *connect.Request[orchestratorv1.PublishReleaseRequest],
) (*connect.Response[orchestratorv1.PublishReleaseResponse], error) {
	msg := req.Msg

	// Skeleton: verify the definition exists and cluster is active.
	def, err := s.store.Definitions().Get(ctx, msg.ReleaseDefinitionId)
	if err == store.ErrNotFound {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("release_definition not found: %s", msg.ReleaseDefinitionId))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("definition lookup: %w", err))
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

// hashRequest computes a deterministic hash of the request for idempotency.
func hashRequest(req *orchestratorv1.CreateOperationRequest) string {
	payload := fmt.Sprintf("%s|%s|%s|%s|%s|%d|%s|%s",
		req.OperationType,
		req.BundleId,
		req.ReleaseDefinitionId,
		req.ValuesRevisionId,
		req.ValuesPatch,
		req.ExpectedCurrentRevision,
		req.Actor.GetUserId(),
		req.Actor.GetOrganization(),
	)
	h := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", h)
}

// Compile-time check: Service implements the Connect handler interface.
var _ orchestratorv1connect.OrchestratorServiceHandler = (*Service)(nil)
