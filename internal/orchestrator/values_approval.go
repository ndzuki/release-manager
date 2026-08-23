package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/contracts"
	"github.com/ndzuki/release-manager/internal/store"
)

type approvalAction string

const (
	approvalActionSubmit  approvalAction = "submit"
	approvalActionApprove approvalAction = "approve"
	approvalActionReject  approvalAction = "reject"

	maxApprovalTextBytes = 1024
)

type approvalActor struct {
	userID string
	orgID  string
	role   store.Role
}

func (s *Service) SubmitValuesRevision(
	ctx context.Context,
	req *connect.Request[orchestratorv1.SubmitValuesRevisionRequest],
) (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error) {
	return s.handleValuesApproval(
		ctx,
		req.Header().Get("Idempotency-Key"),
		approvalActionSubmit,
		req.Msg.GetRevisionId(),
		req.Msg.GetExpectedStateVersion(),
		req.Msg.GetComment(),
		"",
	)
}

func (s *Service) ApproveValuesRevision(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ApproveValuesRevisionRequest],
) (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error) {
	return s.handleValuesApproval(
		ctx,
		req.Header().Get("Idempotency-Key"),
		approvalActionApprove,
		req.Msg.GetRevisionId(),
		req.Msg.GetExpectedStateVersion(),
		req.Msg.GetComment(),
		"",
	)
}

func (s *Service) RejectValuesRevision(
	ctx context.Context,
	req *connect.Request[orchestratorv1.RejectValuesRevisionRequest],
) (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error) {
	return s.handleValuesApproval(
		ctx,
		req.Header().Get("Idempotency-Key"),
		approvalActionReject,
		req.Msg.GetRevisionId(),
		req.Msg.GetExpectedStateVersion(),
		"",
		req.Msg.GetReason(),
	)
}

//nolint:gocyclo // The ordered error model intentionally keeps policy gates explicit.
func (s *Service) handleValuesApproval(
	ctx context.Context,
	idempotencyKey string,
	action approvalAction,
	revisionID string,
	expectedStateVersion int64,
	comment string,
	reason string,
) (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error) {
	ctx = authorization.WithFenceCapture(ctx)
	actorContext, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	if err := validateApprovalInput(action, revisionID, expectedStateVersion, idempotencyKey, comment, reason); err != nil {
		return nil, err
	}

	revision, err := s.store.Values().Get(ctx, revisionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, valuesApprovalError(connect.CodeNotFound, "revision_not_found", fmt.Errorf("values_revision %s not found", revisionID))
		}
		return nil, valuesApprovalError(connect.CodeInternal, "internal_error", errors.New("values revision lookup failed"))
	}
	definition, err := s.store.Definitions().Get(ctx, revision.ReleaseDefinitionID)
	if err != nil {
		return nil, valuesApprovalError(connect.CodeInternal, "internal_error", errors.New("definition lookup failed"))
	}
	actor, err := s.checkApprovalAuth(ctx, revision, definition, actorContext, action)
	if err != nil {
		s.recordFailedApprovalAttempt(ctx, revision, actorContext, action, err)
		return nil, err
	}

	expectedAuthorizationVersion, ok := authorization.SourceVersionFromContext(ctx)
	if !ok {
		return nil, valuesApprovalError(connect.CodeUnavailable, "authorization_snapshot_stale",
			errors.New("authorization snapshot is unavailable"))
	}
	trimmedComment := strings.TrimSpace(comment)
	command := store.ValuesApprovalCommand{
		RevisionID:                   revisionID,
		ExpectedStateVersion:         expectedStateVersion,
		ExpectedAuthorizationVersion: expectedAuthorizationVersion,
		ActorUserID:                  actor.userID,
		ActorOrgID:                   actor.orgID,
		ActorRole:                    actor.role,
		Authorized:                   true,
		Reason:                       strings.TrimSpace(reason),
		RequestID:                    requestIDOrNew(ctx),
		// Scope includes the organization so the same raw key under different
		// tenants never collides (AC-010-05).
		IdempotencyScope:             fmt.Sprintf("%s:%s:%s:%s", actor.orgID, actor.userID, action, revisionID),
		IdempotencyKeyHash:           hashApprovalIdempotencyKey(idempotencyKey),
		RequestHash:                  hashApprovalRequest(action, revisionID, expectedStateVersion, trimmedComment, strings.TrimSpace(reason)),
	}
	if trimmedComment != "" {
		command.Comment = &trimmedComment
	}

	var result *store.ValuesApprovalResult
	switch action {
	case approvalActionSubmit:
		result, err = s.store.ValuesApproval().Submit(ctx, command)
	case approvalActionApprove:
		result, err = s.store.ValuesApproval().Approve(ctx, command)
	case approvalActionReject:
		result, err = s.store.ValuesApproval().Reject(ctx, command)
	default:
		err = errors.New("unsupported approval action")
	}
	if err != nil {
		connectErr := valuesApprovalConnectError(err, revisionID, revision.ReleaseDefinitionID, expectedStateVersion)
		s.recordFailedApprovalAttempt(ctx, revision, actorContext, action, connectErr)
		return nil, connectErr
	}
	return connect.NewResponse(toValuesDecisionResponse(result)), nil
}

// requestIDOrNew returns the trace request_id from ctx when present (set by the
// RequestIDInterceptor), falling back to a fresh UUID for in-process callers that
// bypass the wire chain. Persisted decision/audit records carry the originating
// request_id so approvals can be traced back to the API request (REQ-010/ADR-010).
func requestIDOrNew(ctx context.Context) string {
	if rid := contracts.RequestID(ctx); rid != "" {
		return rid
	}
	return uuid.NewString()
}

func validateApprovalInput(
	action approvalAction,
	revisionID string,
	expectedStateVersion int64,
	idempotencyKey string,
	comment string,
	reason string,
) error {
	if revisionID == "" {
		return valuesApprovalError(connect.CodeInvalidArgument, "invalid_argument", errors.New("revision_id is required"))
	}
	if expectedStateVersion < 1 {
		return valuesApprovalError(connect.CodeInvalidArgument, "invalid_argument", errors.New("invalid expected_state_version"))
	}
	if len(idempotencyKey) > 64 {
		return valuesApprovalError(connect.CodeInvalidArgument, "invalid_argument", errors.New("idempotency key too large"))
	}
	if action == approvalActionReject {
		trimmedReason := strings.TrimSpace(reason)
		if trimmedReason == "" {
			return valuesApprovalError(connect.CodeInvalidArgument, "invalid_argument", errors.New("reason required"))
		}
		if err := validateApprovalText(trimmedReason, "reason"); err != nil {
			return err
		}
		return nil
	}
	if strings.TrimSpace(comment) != "" {
		if err := validateApprovalText(strings.TrimSpace(comment), "comment"); err != nil {
			return err
		}
	}
	return nil
}

func valuesApprovalError(code connect.Code, reason string, err error) error {
	connectErr := connect.NewError(code, err)
	connectErr.Meta().Set("X-Reason-Code", reason)
	return connectErr
}

func validateApprovalText(value, field string) error {
	if !utf8.ValidString(value) {
		return valuesApprovalError(connect.CodeInvalidArgument, "invalid_argument", fmt.Errorf("%s is invalid UTF-8", field))
	}
	if len([]byte(value)) > maxApprovalTextBytes {
		return valuesApprovalError(connect.CodeInvalidArgument, "invalid_argument", fmt.Errorf("%s too large", field))
	}
	if strings.ContainsRune(value, '\x00') || strings.ContainsRune(value, '\uFFFE') || strings.ContainsRune(value, '\uFFFF') {
		return valuesApprovalError(connect.CodeInvalidArgument, "invalid_argument", fmt.Errorf("%s contains forbidden characters", field))
	}
	return nil
}

//nolint:gocyclo // Authorization follows the required resource-lifecycle and permission precedence explicitly.
func (s *Service) checkApprovalAuth(
	ctx context.Context,
	revision *store.ValuesRevision,
	definition *store.ReleaseDefinition,
	actorContext authctx.Actor,
	action approvalAction,
) (approvalActor, error) {
	if definition.Status != store.DefStatusActive {
		return approvalActor{}, valuesApprovalError(connect.CodeFailedPrecondition, "invalid_revision_state",
			fmt.Errorf("definition %s is %s", definition.ID, definition.Status))
	}
	if definition.OwnerOrganizationID == nil || *definition.OwnerOrganizationID == "" {
		return approvalActor{}, valuesApprovalError(connect.CodeFailedPrecondition, "release_definition_owner_unresolved",
			fmt.Errorf("definition %s owner organization must be set before approval", definition.ID))
	}
	if actorContext.OrganizationID != *definition.OwnerOrganizationID {
		return approvalActor{}, valuesApprovalError(connect.CodePermissionDenied, "not_authorized", errors.New("actor not authorized"))
	}
	customer, err := s.store.Customers().Get(ctx, definition.CustomerID)
	if err != nil {
		return approvalActor{}, authorizationUnavailableError(err)
	}
	if customer.Status != store.CustomerActive {
		return approvalActor{}, valuesApprovalError(connect.CodeFailedPrecondition, "customer_disabled", errors.New("customer is disabled"))
	}
	organization, err := s.store.Organizations().Get(ctx, actorContext.OrganizationID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return approvalActor{}, valuesApprovalError(connect.CodePermissionDenied, "not_authorized", errors.New("actor not authorized"))
		}
		return approvalActor{}, authorizationUnavailableError(err)
	}
	if organization.Status != store.OrgActive {
		return approvalActor{}, valuesApprovalError(connect.CodeFailedPrecondition, "organization_disabled", errors.New("organization is disabled"))
	}
	binding, err := s.store.Bindings().GetByOrgAndCustomer(ctx, actorContext.OrganizationID, definition.CustomerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return approvalActor{}, valuesApprovalError(connect.CodeFailedPrecondition, "binding_revoked", errors.New("organization-customer binding is revoked"))
		}
		return approvalActor{}, authorizationUnavailableError(err)
	}
	if binding.Status != store.BindingActive {
		return approvalActor{}, valuesApprovalError(connect.CodeFailedPrecondition, "binding_revoked", errors.New("organization-customer binding is revoked"))
	}
	member, err := s.store.OrgMembers().Get(ctx, actorContext.OrganizationID, actorContext.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return approvalActor{}, valuesApprovalError(connect.CodePermissionDenied, "membership_inactive", errors.New("actor has no active membership"))
		}
		return approvalActor{}, authorizationUnavailableError(err)
	}
	if action == approvalActionSubmit {
		if revision.CreatedByUserID != actorContext.UserID {
			return approvalActor{}, valuesApprovalError(connect.CodePermissionDenied, "not_authorized", errors.New("actor not authorized"))
		}
	} else if revision.CreatedByUserID == actorContext.UserID {
		return approvalActor{}, valuesApprovalError(connect.CodePermissionDenied, "self_approval_forbidden",
			errors.New("revision creator cannot approve or reject own revision"))
	}
	if s.authorizer == nil {
		return approvalActor{}, valuesApprovalError(connect.CodeUnavailable, "authorization_snapshot_stale",
			errors.New("authorization snapshot is unavailable"))
	}
	capability := store.AuthorizationApproveValues
	if action == approvalActionSubmit {
		capability = store.AuthorizationCreateValues
	}
	if err := s.authorizer.AuthorizeWrite(ctx, actorContext, definition.CustomerID, capability); err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return approvalActor{}, valuesApprovalError(connect.CodePermissionDenied, "role_insufficient",
				fmt.Errorf("actor role %s is insufficient for %s", member.Role, action))
		}
		return approvalActor{}, err
	}
	return approvalActor{userID: actorContext.UserID, orgID: actorContext.OrganizationID, role: member.Role}, nil
}

func authorizationUnavailableError(error) error {
	return valuesApprovalError(connect.CodeUnavailable, "dependency_unavailable",
		errors.New("authorization service unavailable"))
}

func valuesApprovalConnectError(
	err error,
	revisionID string,
	definitionID string,
	expectedStateVersion int64,
) error {
	var versionErr *store.StateVersionConflictError
	var stateErr *store.InvalidValuesStateError
	switch {
	case errors.As(err, &versionErr):
		return valuesApprovalError(connect.CodeAborted, "optimistic_lock_conflict",
			fmt.Errorf("state version conflict: expected %d, current %d", versionErr.Expected, versionErr.Current))
	case errors.As(err, &stateErr):
		return valuesApprovalError(connect.CodeFailedPrecondition, "invalid_revision_state",
			fmt.Errorf("revision %s is %s, expected %s", revisionID, stateErr.Actual, stateErr.Expected))
	case errors.Is(err, store.ErrAuthorizationStale):
		return valuesApprovalError(connect.CodeUnavailable, "authorization_snapshot_stale", errors.New("authorization snapshot is stale"))
	case errors.Is(err, store.ErrApprovalPending):
		return valuesApprovalError(connect.CodeFailedPrecondition, "approval_already_pending",
			fmt.Errorf("another revision is already pending approval for definition %s", definitionID))
	case errors.Is(err, store.ErrIdempotencyConflict):
		return valuesApprovalError(connect.CodeAlreadyExists, "idempotency_conflict",
			errors.New("idempotency key conflict: different request for same scope and key"))
	case errors.Is(err, store.ErrNotFound):
		return valuesApprovalError(connect.CodeNotFound, "revision_not_found", fmt.Errorf("values_revision %s not found", revisionID))
	case errors.Is(err, store.ErrOptimisticLock), isSQLiteBusy(err):
		return valuesApprovalError(connect.CodeAborted, "optimistic_lock_conflict",
			fmt.Errorf("state version conflict: expected %d", expectedStateVersion))
	case errors.Is(err, store.ErrNotAuthorized):
		return valuesApprovalError(connect.CodePermissionDenied, "not_authorized", errors.New("actor not authorized"))
	default:
		return valuesApprovalError(connect.CodeInternal, "internal_error", errors.New("values approval failed"))
	}
}

func isSQLiteBusy(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "busy") || strings.Contains(message, "locked") || strings.Contains(message, "deadlock")
}

func (s *Service) recordFailedApprovalAttempt(
	ctx context.Context,
	revision *store.ValuesRevision,
	actor authctx.Actor,
	action approvalAction,
	failure error,
) {
	code := connect.CodeOf(failure)
	if code == connect.CodeUnavailable || code == connect.CodeInternal {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"event_id":              uuid.New().String(),
		"event_type":            "ValuesRevisionApprovalAttemptRejected",
		"revision_id":           revision.ID,
		"release_definition_id": revision.ReleaseDefinitionID,
		"organization_id":       actor.OrganizationID,
		"actor_user_id":         actor.UserID,
		"request_id":            requestIDOrNew(ctx),
		"action":                action,
		"connect_code":          connect.CodeOf(failure).String(),
		"reason_code":           approvalErrorReason(failure),
	})
	if err != nil {
		s.logger.Warn("marshal values approval attempt audit", "revision_id", revision.ID, "error", err)
		return
	}
	entry := &store.ApprovalOutboxEntry{
		ID:          uuid.New().String(),
		EventType:   "ValuesRevisionApprovalAttemptRejected",
		PayloadJSON: payload,
	}
	if err := s.store.ValuesApproval().RecordAttempt(ctx, entry); err != nil {
		s.logger.Warn("record values approval attempt audit", "revision_id", revision.ID, "error", err)
	}
}
func approvalErrorReason(err error) string {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		if reason := connectErr.Meta().Get("X-Reason-Code"); reason != "" {
			return reason
		}
	}
	return connect.CodeOf(err).String()
}

func hashApprovalIdempotencyKey(key string) string {
	if key == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

func hashApprovalRequest(
	action approvalAction,
	revisionID string,
	expectedStateVersion int64,
	comment string,
	reason string,
) string {
	payload := fmt.Sprintf("%s|%s|%d|%s|%s", action, revisionID, expectedStateVersion, comment, reason)
	hash := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(hash[:])
}

func toValuesDecisionResponse(result *store.ValuesApprovalResult) *orchestratorv1.ValuesRevisionDecisionResponse {
	return &orchestratorv1.ValuesRevisionDecisionResponse{
		Revision:              toProtoValuesRevision(result.Revision),
		PreviousState:         valuesStatusToProto(result.PreviousState),
		NewState:              valuesStatusToProto(result.NewState),
		DecidedAt:             timestamppb.New(result.DecidedAt),
		SupersededRevisionIds: result.SupersededRevisionIDs,
	}
}

func toProtoValuesRevision(revision *store.ValuesRevision) *commonv1.ValuesRevision {
	if revision == nil {
		return nil
	}
	result := &commonv1.ValuesRevision{
		Id:                  revision.ID,
		ReleaseDefinitionId: revision.ReleaseDefinitionID,
		Version:             revision.Version,
		CanonicalDocument:   revision.CanonicalDocument,
		CreatedAt:           timestamppb.New(revision.CreatedAt),
		Status:              valuesStatusToProto(revision.Status),
		Digest:              revision.Digest,
		ParentRevisionId:    revision.ParentRevisionID,
		StateVersion:        revision.StateVersion,
		CreatedByUserId:     revision.CreatedByUserID,
		SecretRefs:          make([]*commonv1.SecretRef, 0, len(revision.SecretRefs)),
		// REQ-079 D10: convergence bindings.
		ConvergenceTaskIds: revision.ConvergenceTaskIds,
		LockedPaths:        revision.LockedPaths,
	}
	for _, ref := range revision.SecretRefs {
		result.SecretRefs = append(result.SecretRefs, &commonv1.SecretRef{Path: ref.Path, Name: ref.Name, Key: ref.Key})
	}
	if revision.SubmittedAt != nil {
		result.SubmittedAt = timestamppb.New(*revision.SubmittedAt)
	}
	if revision.DecidedAt != nil {
		result.DecidedAt = timestamppb.New(*revision.DecidedAt)
	}
	return result
}

func valuesStatusToProto(status store.ValuesStatus) commonv1.ValuesStatus {
	switch status {
	case store.ValuesStatusDraft:
		return commonv1.ValuesStatus_VALUES_STATUS_DRAFT
	case store.ValuesStatusPendingApproval:
		return commonv1.ValuesStatus_VALUES_STATUS_PENDING_APPROVAL
	case store.ValuesStatusApproved:
		return commonv1.ValuesStatus_VALUES_STATUS_APPROVED
	case store.ValuesStatusRejected:
		return commonv1.ValuesStatus_VALUES_STATUS_REJECTED
	case store.ValuesStatusSuperseded:
		return commonv1.ValuesStatus_VALUES_STATUS_SUPERSEDED
	case store.ValuesStatusDiscarded:
		return commonv1.ValuesStatus_VALUES_STATUS_DISCARDED
	default:
		return commonv1.ValuesStatus_VALUES_STATUS_UNSPECIFIED
	}
}
