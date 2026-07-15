// Package orchestrator implements the release orchestration gRPC service.
package orchestrator

import (
	"context"
	"crypto/sha256"

	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/orchestrator/operation"
	"github.com/ndzuki/release-manager/internal/store"
)

// Service implements the OrchestratorService gRPC server.
type Service struct {
	orchestratorv1.UnimplementedOrchestratorServiceServer
	store  store.Store
	logger *slog.Logger
}

// NewService creates a new orchestrator gRPC service.
func NewService(st store.Store, logger *slog.Logger) *Service {
	return &Service{store: st, logger: logger}
}

// CreateOperation creates a new release operation from the given request.
// Implements REQ-067 validation rules and REQ-023 idempotency.
func (s *Service) CreateOperation(ctx context.Context, req *orchestratorv1.CreateOperationRequest) (*orchestratorv1.CreateOperationResponse, error) {
	// 1. Idempotency check (REQ-023 AC-023-02)
	if req.IdempotencyKey != "" {
		existing, err := s.store.Operations().GetByIdempotencyKey(ctx, req.IdempotencyKey)
		if err == nil {
			s.logger.Info("idempotent operation found", "key", req.IdempotencyKey, "op_id", existing.ID)
			return s.toResponse(existing), nil
		}
		if err != store.ErrNotFound {
			return nil, status.Errorf(codes.Internal, "idempotency lookup: %v", err)
		}
		// not found → proceed
	}

	// 2. Validate operation type
	opType := store.OperationType(req.OperationType)
	if !opType.Valid() {
		return nil, status.Errorf(codes.InvalidArgument, "invalid operation_type: %s", req.OperationType)
	}

	// 3. Lookup release definition
	def, err := s.store.Definitions().Get(ctx, req.ReleaseDefinitionId)
	if err == store.ErrNotFound {
		return nil, status.Errorf(codes.NotFound, "release_definition not found: %s", req.ReleaseDefinitionId)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "definition lookup: %v", err)
	}

	// Validate definition status
	if def.Status != store.DefStatusActive {
		return nil, status.Errorf(codes.FailedPrecondition, "release_definition %s is %s", def.ID, def.Status)
	}

	// 4. Release busy check (REQ-023 AC-023-03, AC-023-06, AC-023-07)
	active, err := s.store.Operations().HasActiveForDefinition(ctx, req.ReleaseDefinitionId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "active check: %v", err)
	}
	if active {
		return nil, status.Errorf(codes.FailedPrecondition, "release_busy: definition %s has active operation", req.ReleaseDefinitionId)
	}

	// 5. Build operation request hash for idempotency
	reqHash := hashRequest(req)

	// 6. Build domain Operation
	now := time.Now().UTC()
	op := &store.Operation{
		ID:                  uuid.New().String(),
		OperationType:       opType,
		Status:              operation.InitialStatus(),
		ReleaseDefinitionID: req.ReleaseDefinitionId,
		IdempotencyKey:      req.IdempotencyKey,
		RequestHash:         reqHash,
		BundleID:            req.BundleId,
		ValuesRevisionID:    req.ValuesRevisionId,
		ExpectedRevision:    int(req.ExpectedCurrentRevision),
		ValuesPatch:         []byte(req.ValuesPatch),
		Actor: store.ActorContext{
			UserID:       req.Actor.GetUserId(),
			Organization: req.Actor.GetOrganization(),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// 7. Persist
	if err := s.store.Operations().Create(ctx, op); err != nil {
		return nil, status.Errorf(codes.Internal, "create operation: %v", err)
	}

	// 8. Trigger preflight transition (async in production; synchronous for now)
	//    Standard ops go pending→preflight, EMERGENCY goes pending→queued
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

	return s.toResponse(op), nil
}

// PublishRelease triggers the release pipeline for a definition (skeleton).
func (s *Service) PublishRelease(ctx context.Context, req *orchestratorv1.PublishReleaseRequest) (*orchestratorv1.PublishReleaseResponse, error) {
	// Skeleton: verify the definition exists and return not-yet-implemented status.
	_, err := s.store.Definitions().Get(ctx, req.ReleaseDefinitionId)
	if err == store.ErrNotFound {
		return nil, status.Errorf(codes.NotFound, "release_definition not found: %s", req.ReleaseDefinitionId)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "definition lookup: %v", err)
	}

	s.logger.Info("publish release requested (skeleton)", "definition", req.ReleaseDefinitionId)
	return &orchestratorv1.PublishReleaseResponse{
		OperationId: "",
		Status:      "not_implemented",
	}, nil
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
	// Hash the structured fields that form the request identity.
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

// Compile-time check: Service implements the gRPC server interface.
var _ orchestratorv1.OrchestratorServiceServer = (*Service)(nil)
