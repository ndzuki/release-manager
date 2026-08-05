package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/store"
)

const (
	prepareSessionTTL = 15 * time.Minute
	maxTaskIDs        = 50
	minTaskIDs        = 1
	prepareTokenBytes = 32 // 256-bit
)

// CreatePrepareSession handles the create prepare session RPC.
//
//nolint:gocyclo // The ordered authorization, chain-head, and task-selection checks mirror REQ-018.
func (s *Service) CreatePrepareSession(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreatePrepareSessionRequest],
) (*connect.Response[orchestratorv1.CreatePrepareSessionResponse], error) {
	ctx = authorization.WithFenceCapture(ctx)
	msg := req.Msg

	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, valuesRevisionError(connect.CodeUnauthenticated, "authentication_required", errors.New("authentication required"))
	}

	if msg.GetReleaseDefinitionId() == "" {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "task_invalid",
			errors.New("release_definition_id is required"))
	}
	if len(msg.GetTaskIds()) < minTaskIDs || len(msg.GetTaskIds()) > maxTaskIDs {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "task_invalid",
			fmt.Errorf("task_ids must contain between %d and %d values", minTaskIDs, maxTaskIDs))
	}
	taskIDs, unique := uniqueTaskIDs(msg.GetTaskIds())
	if !unique {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "task_invalid",
			errors.New("task_ids must not contain duplicates or empty values"))
	}

	// Resolve definition for authorization
	def, err := s.store.Definitions().Get(ctx, msg.GetReleaseDefinitionId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, valuesRevisionError(connect.CodeNotFound, "release_definition_not_found",
				fmt.Errorf("release_definition not found: %s", msg.GetReleaseDefinitionId()))
		}
		return nil, s.stableInternalError("get release definition", err)
	}

	// Authorization
	if def.OwnerOrganizationID == nil || *def.OwnerOrganizationID == "" {
		return nil, valuesRevisionError(connect.CodeFailedPrecondition, "release_definition_owner_unresolved",
			errors.New("release definition owner organization is unresolved"))
	}
	if actor.OrganizationID != *def.OwnerOrganizationID {
		return nil, valuesRevisionError(connect.CodePermissionDenied, "permission_denied", errors.New("permission denied"))
	}
	if s.authorizer == nil {
		return nil, valuesRevisionError(connect.CodeUnavailable, "authorization_snapshot_stale",
			errors.New("authorization snapshot is unavailable"))
	}
	if err := s.authorizer.AuthorizeWrite(ctx, actor, def.CustomerID, store.AuthorizationCreateValues); err != nil {
		return nil, valuesAuthorizationError(err)
	}

	// Check customer not disabled
	if err := s.checkCustomerNotDisabled(ctx, def.CustomerID); err != nil {
		return nil, err
	}

	// Lock the current chain head. A caller-provided token must match it exactly.
	var parentRevisionID string
	var parentVersion int64
	latest, err := s.store.Values().GetLatest(ctx, msg.GetReleaseDefinitionId())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, s.stableInternalError("get latest revision", err)
	}
	if latest != nil {
		parentRevisionID = latest.ID
		parentVersion = latest.Version
	}
	if msg.GetExpectedParentVersion() != parentVersion {
		return nil, valuesRevisionError(connect.CodeAborted, "parent_conflict",
			fmt.Errorf("expected parent version %d, current %d", msg.GetExpectedParentVersion(), parentVersion))
	}
	// Validate every selected task and compute stable locked paths.
	lockedPaths, err := s.computeLockedPaths(ctx, msg.GetReleaseDefinitionId(), taskIDs)
	if err != nil {
		return nil, err
	}

	// Generate token
	tokenBytes := make([]byte, prepareTokenBytes)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, s.stableInternalError("generate prepare token", err)
	}
	token := hex.EncodeToString(tokenBytes)
	tokenHash := hashPrepareToken(token)
	lockedPathHash := store.LockedPathHash(lockedPaths)

	now := time.Now().UTC()
	session := &store.PrepareSession{
		TokenHash:           tokenHash,
		ActorUserID:         actor.UserID,
		OrganizationID:      actor.OrganizationID,
		ReleaseDefinitionID: msg.GetReleaseDefinitionId(),
		ParentRevisionID:    parentRevisionID,
		ParentVersion:       parentVersion,
		TaskIDs:             taskIDs,
		LockedPaths:         lockedPaths,
		LockedPathHash:      lockedPathHash,
		ExpiresAt:           now.Add(prepareSessionTTL),
		CreatedAt:           now,
	}

	expectedAuthorizationVersion, ok := authorization.SourceVersionFromContext(ctx)
	if !ok {
		return nil, valuesRevisionError(connect.CodeUnavailable, "authorization_snapshot_stale",
			errors.New("authorization snapshot is unavailable"))
	}
	if err := s.store.PrepareSessions().Create(ctx, session, expectedAuthorizationVersion); err != nil {
		return nil, s.stableInternalError("create prepare session", err)
	}

	return connect.NewResponse(&orchestratorv1.CreatePrepareSessionResponse{
		PrepareToken:     token,
		ExpiresAt:        timestamppb.New(session.ExpiresAt),
		ParentRevisionId: parentRevisionID,
		ParentVersion:    parentVersion,
		LockedPaths:      lockedPaths,
	}), nil
}

// GetPrepareSession handles the get prepare session RPC.
func (s *Service) GetPrepareSession(
	ctx context.Context,
	req *connect.Request[orchestratorv1.GetPrepareSessionRequest],
) (*connect.Response[orchestratorv1.GetPrepareSessionResponse], error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, valuesRevisionError(connect.CodeUnauthenticated, "authentication_required", errors.New("authentication required"))
	}
	msg := req.Msg
	if msg.GetPrepareToken() == "" {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("prepare_token is required"))
	}

	tokenHash := hashPrepareToken(msg.GetPrepareToken())
	session, err := s.store.PrepareSessions().Get(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, valuesRevisionError(connect.CodeNotFound, "prepare_token_not_found", errors.New("prepare token not found"))
		}
		return nil, s.stableInternalError("get prepare session", err)
	}
	if actor.UserID != session.ActorUserID || actor.OrganizationID != session.OrganizationID {
		return nil, valuesRevisionError(connect.CodePermissionDenied, "permission_denied", errors.New("permission denied"))
	}
	// Reads require a real-time membership query (REQ-018 安全边界), so a
	// revoked org membership or binding blocks session reads immediately.
	if err := s.authorizeValuesRead(ctx, session.ReleaseDefinitionID); err != nil {
		return nil, err
	}

	if time.Now().UTC().After(session.ExpiresAt) {
		return nil, valuesRevisionError(connect.CodeFailedPrecondition, "prepare_token_expired", errors.New("prepare_token_expired"))
	}

	// Derive document from parent revision. The parent is pinned by an ON
	// DELETE RESTRICT foreign key, so a missing parent means data
	// inconsistency — never a silent empty document.
	document := "{}"
	if session.ParentRevisionID != "" {
		parent, err := s.store.Values().Get(ctx, session.ParentRevisionID)
		if err != nil {
			return nil, s.stableInternalError("get prepare session parent", err)
		}
		if parent.CanonicalDocument != nil {
			document = string(parent.CanonicalDocument)
		}
	}

	return connect.NewResponse(&orchestratorv1.GetPrepareSessionResponse{
		ReleaseDefinitionId: session.ReleaseDefinitionID,
		ParentRevisionId:    session.ParentRevisionID,
		Document:            document,
		LockedPaths:         session.LockedPaths,
		ExpiresAt:           timestamppb.New(session.ExpiresAt),
	}), nil
}

func (s *Service) computeLockedPaths(ctx context.Context, definitionID string, taskIDs []string) ([]string, error) {
	tasks := make([]*store.ConvergenceTask, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		task, err := s.store.ConvergenceTasks().Get(ctx, taskID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, valuesRevisionError(connect.CodeInvalidArgument, "task_invalid",
					fmt.Errorf("convergence task %s not found", taskID))
			}
			return nil, s.stableInternalError("get convergence task", fmt.Errorf("task %s: %w", taskID, err))
		}
		if task.ReleaseDefinitionID != definitionID || task.Status != "pending_promotion" {
			return nil, valuesRevisionError(connect.CodeInvalidArgument, "task_invalid",
				fmt.Errorf("convergence task %s is not pending for release definition", taskID))
		}
		if task.ActiveRevisionID != nil {
			return nil, valuesRevisionError(connect.CodeAlreadyExists, "convergence_revision_exists",
				fmt.Errorf("convergence task %s is already bound", taskID))
		}
		tasks = append(tasks, task)
	}
	paths, err := store.LockedPathsForTasks(taskIDs, tasks)
	if err != nil {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "task_invalid", err)
	}
	return paths, nil
}

// --- helpers ---

func uniqueTaskIDs(items []string) ([]string, bool) {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" {
			return nil, false
		}
		if _, exists := seen[item]; exists {
			return nil, false
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result, true
}
