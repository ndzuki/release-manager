package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/operator/commandtype"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/values"
)

const (
	defaultMaxDocumentBytes = 1 << 20 // 1 MiB
	maxPageSize             = 100
	idempotencyTTL          = 24 * time.Hour
	secretMetadataTimeout      = 15 * time.Second
	secretMetadataPollInterval = 50 * time.Millisecond
)


// ValuesConfig controls immutable document validation.
type ValuesConfig struct {
	MaxDocumentBytes int64
	SecretPatterns   []string
}

// DefaultValuesConfig returns safe validation defaults.
func DefaultValuesConfig() ValuesConfig {
	return ValuesConfig{
		MaxDocumentBytes: defaultMaxDocumentBytes,
		SecretPatterns:   []string{},
	}
}

// WithDefaults fills omitted ValuesRevision settings.
func (c ValuesConfig) WithDefaults() ValuesConfig {
	if c.MaxDocumentBytes <= 0 {
		c.MaxDocumentBytes = defaultMaxDocumentBytes
	}
	if c.SecretPatterns == nil {
		c.SecretPatterns = []string{}
	}
	return c
}

// CreateValuesRevision handles the create values revision RPC.
//
//nolint:gocyclo // The ordered validation pipeline mirrors the REQ-018 contract.
func (s *Service) CreateValuesRevision(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateValuesRevisionRequest],
) (*connect.Response[orchestratorv1.CreateValuesRevisionResponse], error) {
	ctx = authorization.WithFenceCapture(ctx)
	msg := req.Msg
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, valuesRevisionError(connect.CodeUnauthenticated, "authentication_required", errors.New("authentication required"))
	}
	if msg.GetReleaseDefinitionId() == "" {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("release_definition_id is required"))
	}
	if msg.GetDocument() == "" {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("document is required"))
	}
	idempotencyKey := req.Header().Get("Idempotency-Key")
	if err := validateValuesIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	maxSize := s.valuesMaxDocumentBytes()
	if int64(len(msg.GetDocument())) > maxSize {
		return nil, valuesRevisionError(connect.CodeResourceExhausted, "size_exceeded", errors.New("size_exceeded"))
	}

	definition, err := s.store.Definitions().Get(ctx, msg.GetReleaseDefinitionId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, valuesRevisionError(connect.CodeNotFound, "release_definition_not_found", errors.New("release_definition_not_found"))
	}
	if err != nil {
		return nil, s.stableInternalError("get release definition", err)
	}
	if err := checkDefinitionOperable(definition); err != nil {
		return nil, err
	}
	if err := s.checkCustomerNotDisabled(ctx, definition.CustomerID); err != nil {
		return nil, err
	}
	if _, err := s.authorizeValuesWrite(ctx, actor, definition); err != nil {
		return nil, err
	}

	var prepareSession *store.PrepareSession
	if msg.GetPrepareToken() != "" {
		prepareSession, err = s.store.PrepareSessions().Get(ctx, hashPrepareToken(msg.GetPrepareToken()))
		if errors.Is(err, store.ErrNotFound) {
			return nil, valuesRevisionError(connect.CodeNotFound, "prepare_token_not_found", errors.New("prepare token not found"))
		}
		if err != nil {
			return nil, s.stableInternalError("get prepare session", err)
		}
		if prepareSession.ActorUserID != actor.UserID || prepareSession.OrganizationID != actor.OrganizationID ||
			prepareSession.ReleaseDefinitionID != definition.ID {
			return nil, valuesRevisionError(connect.CodePermissionDenied, "permission_denied", errors.New("permission denied"))
		}
		// An expired token can never have produced a successful create
		// (consumption requires a live session), so it is safe to reject here —
		// before document validation — without breaking idempotent replays
		// (REQ-018 validation order 2, AC-018-09).
		if !prepareSession.ExpiresAt.After(time.Now().UTC()) {
			return nil, valuesRevisionError(connect.CodeFailedPrecondition, "prepare_token_expired", errors.New("prepare_token_expired"))
		}
		// Expiry and consumption are enforced inside the store transaction AFTER
		// the idempotency replay lookup, so a replay of an already-succeeded
		// converged create returns the first result (REQ-010 D-9) instead of
		// being blocked by the consumed/expired token here.
		if msg.GetParentRevisionId() != "" && msg.GetParentRevisionId() != prepareSession.ParentRevisionID {
			return nil, valuesRevisionError(connect.CodeAborted, "parent_conflict", errors.New("parent_conflict"))
		}
		if msg.GetExpectedParentVersion() != 0 && msg.GetExpectedParentVersion() != prepareSession.ParentVersion {
			return nil, valuesRevisionError(connect.CodeAborted, "parent_conflict", errors.New("parent_conflict"))
		}
	}

	storeRefs := make([]store.SecretRef, 0, len(msg.GetSecretRefs()))
	validationRefs := make([]values.SecretRef, 0, len(msg.GetSecretRefs()))
	for _, ref := range msg.GetSecretRefs() {
		if ref == nil {
			return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_secret_ref", errors.New("invalid_secret_ref"))
		}
		storeRef := store.SecretRef{Path: ref.GetPath(), Name: ref.GetName(), Key: ref.GetKey()}
		storeRefs = append(storeRefs, storeRef)
		validationRefs = append(validationRefs, values.SecretRef{Path: storeRef.Path, Name: storeRef.Name, Key: storeRef.Key})
	}
	validated, err := values.ValidateWithRefs(
		[]byte(msg.GetDocument()),
		maxSize,
		s.valuesConfig.WithDefaults().SecretPatterns,
		validationRefs,
	)
	if err != nil {
		return nil, valuesValidationConnectError(err)
	}

	parentRevisionID := msg.GetParentRevisionId()
	expectedParentVersion := msg.GetExpectedParentVersion()
	if prepareSession != nil {
		parentRevisionID = prepareSession.ParentRevisionID
		expectedParentVersion = prepareSession.ParentVersion
	}

	expectedAuthorizationVersion, ok := authorization.SourceVersionFromContext(ctx)
	if !ok {
		return nil, valuesRevisionError(connect.CodeFailedPrecondition, "authorization_snapshot_stale", errors.New("authorization_snapshot_stale"))
	}
	now := time.Now().UTC()
	revision := &store.ValuesRevision{
		ID:                  uuid.NewString(),
		ReleaseDefinitionID: definition.ID,
		Status:              store.ValuesStatusDraft,
		CanonicalDocument:   validated.Canonical,
		Digest:              validated.Digest,
		ParentRevisionID:    parentRevisionID,
		SecretRefs:          storeRefs,
		CreatedByUserID:     actor.UserID,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	command := store.CreateValuesDraftCommand{
		Revision:                     revision,
		PrepareTokenHash:             prepareTokenHash(prepareSession),
		ExpectedParentVersion:        expectedParentVersion,
		ExpectedLockedPathHash:       prepareLockedPathHash(prepareSession),
		ExpectedAuthorizationVersion: expectedAuthorizationVersion,
		ActorUserID:                  actor.UserID,
		OrganizationID:               actor.OrganizationID,
		RequestID:                    uuid.NewString(),
		IdempotencyScope:             idempotencyCreateScope(actor.UserID, definition.ID),
		IdempotencyKeyHash:           hashIdempotencyKey(idempotencyKey),
		RequestHash:                  hashCreateRequest(msg, storeRefs),
		IdempotencyExpiresAt:         now.Add(idempotencyTTL),
	}
	result, err := s.store.ValuesLifecycle().CreateDraft(ctx, command)
	if err != nil {
		return nil, valuesLifecycleConnectError(err, definition.ID)
	}
	return connect.NewResponse(&orchestratorv1.CreateValuesRevisionResponse{
		Revision: toProtoValuesRevision(result.Revision),
		Created:  !result.Replayed,
	}), nil
}

// GetValuesRevision handles the get values revision RPC.
func (s *Service) GetValuesRevision(
	ctx context.Context,
	req *connect.Request[orchestratorv1.GetValuesRevisionRequest],
) (*connect.Response[commonv1.ValuesRevision], error) {
	if req.Msg.GetRevisionId() == "" {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("revision_id is required"))
	}
	revision, err := s.store.Values().Get(ctx, req.Msg.GetRevisionId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, valuesRevisionError(connect.CodeNotFound, "revision_not_found", errors.New("revision_not_found"))
	}
	if err != nil {
		return nil, s.stableInternalError("get values revision", err)
	}
	if err := s.authorizeValuesRead(ctx, revision.ReleaseDefinitionID); err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoValuesRevision(revision)), nil
}

// ListValuesRevisions handles the list values revisions RPC.
func (s *Service) ListValuesRevisions(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListValuesRevisionsRequest],
) (*connect.Response[orchestratorv1.ListValuesRevisionsResponse], error) {
	msg := req.Msg
	if msg.GetReleaseDefinitionId() == "" {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("release_definition_id is required"))
	}
	if msg.GetPageSize() < 0 || msg.GetPageSize() > maxPageSize {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("page_size must be between 0 and 100"))
	}
	if err := s.authorizeValuesRead(ctx, msg.GetReleaseDefinitionId()); err != nil {
		return nil, err
	}
	filter := store.ValuesListFilter{
		ReleaseDefinitionID: msg.GetReleaseDefinitionId(),
		PageSize:            int(msg.GetPageSize()),
		Cursor:              msg.GetCursor(),
	}
	if msg.GetStatus() != commonv1.ValuesStatus_VALUES_STATUS_UNSPECIFIED {
		filter.Status = protoStatusToStore(msg.GetStatus())
		if filter.Status == "" {
			return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("invalid values status"))
		}
	}
	page, err := s.store.Values().ListPage(ctx, filter)
	if errors.Is(err, store.ErrInvalidCursor) {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_cursor", errors.New("invalid cursor"))
	}
	if err != nil {
		return nil, s.stableInternalError("list values revisions", err)
	}
	items := make([]*commonv1.ValuesRevision, 0, len(page.Items))
	for _, revision := range page.Items {
		items = append(items, toProtoValuesRevision(revision))
	}
	return connect.NewResponse(&orchestratorv1.ListValuesRevisionsResponse{
		Items:      items,
		NextCursor: page.NextCursor,
	}), nil
}

// DiscardValuesRevision handles the discard values revision RPC.
func (s *Service) DiscardValuesRevision(
	ctx context.Context,
	req *connect.Request[orchestratorv1.DiscardValuesRevisionRequest],
) (*connect.Response[orchestratorv1.ValuesRevisionDecisionResponse], error) {
	ctx = authorization.WithFenceCapture(ctx)
	msg := req.Msg
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, valuesRevisionError(connect.CodeUnauthenticated, "authentication_required", errors.New("authentication required"))
	}
	if msg.GetRevisionId() == "" {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("revision_id is required"))
	}
	if msg.GetExpectedStateVersion() < 1 {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("expected_state_version must be >= 1"))
	}
	idempotencyKey := req.Header().Get("Idempotency-Key")
	if err := validateValuesIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	comment, err := normalizedValuesComment(msg.GetComment())
	if err != nil {
		return nil, err
	}
	revision, err := s.store.Values().Get(ctx, msg.GetRevisionId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, valuesRevisionError(connect.CodeNotFound, "revision_not_found", errors.New("revision_not_found"))
	}
	if err != nil {
		return nil, s.stableInternalError("get values revision", err)
	}
	definition, err := s.store.Definitions().Get(ctx, revision.ReleaseDefinitionID)
	if err != nil {
		return nil, s.stableInternalError("get release definition", err)
	}
	if err := s.checkCustomerNotDisabled(ctx, definition.CustomerID); err != nil {
		return nil, err
	}
	role, err := s.authorizeValuesWrite(ctx, actor, definition)
	if err != nil {
		return nil, err
	}
	expectedAuthorizationVersion, ok := authorization.SourceVersionFromContext(ctx)
	if !ok {
		return nil, valuesRevisionError(connect.CodeFailedPrecondition, "authorization_snapshot_stale", errors.New("authorization_snapshot_stale"))
	}
	now := time.Now().UTC()
	result, err := s.store.ValuesLifecycle().Discard(ctx, store.DiscardValuesCommand{
		RevisionID:                   revision.ID,
		ExpectedStateVersion:         msg.GetExpectedStateVersion(),
		ExpectedAuthorizationVersion: expectedAuthorizationVersion,
		ActorUserID:                  actor.UserID,
		ActorOrgID:                   actor.OrganizationID,
		ActorRole:                    role,
		Comment:                      comment,
		RequestID:                    uuid.NewString(),
		IdempotencyScope:             idempotencyDiscardScope(actor.UserID, revision.ID),
		IdempotencyKeyHash:           hashIdempotencyKey(idempotencyKey),
		RequestHash:                  hashDiscardRequest(msg),
		IdempotencyExpiresAt:         now.Add(idempotencyTTL),
	})
	if err != nil {
		return nil, discardConnectError(err, revision.ID)
	}
	return connect.NewResponse(toValuesDiscardDecisionResponse(result)), nil
}

func (s *Service) valuesMaxDocumentBytes() int64 {
	return s.valuesConfig.WithDefaults().MaxDocumentBytes
}

func (s *Service) authorizeValuesWrite(
	ctx context.Context,
	actor authctx.Actor,
	definition *store.ReleaseDefinition,
) (store.Role, error) {
	if definition.OwnerOrganizationID == nil || *definition.OwnerOrganizationID == "" {
		return "", valuesRevisionError(connect.CodeFailedPrecondition, "release_definition_owner_unresolved",
			errors.New("release definition owner organization is unresolved"))
	}
	if actor.OrganizationID != *definition.OwnerOrganizationID {
		return "", valuesRevisionError(connect.CodePermissionDenied, "permission_denied", errors.New("permission denied"))
	}
	member, err := s.store.OrgMembers().Get(ctx, actor.OrganizationID, actor.UserID)
	if err != nil {
		return "", valuesRevisionError(connect.CodePermissionDenied, "permission_denied", errors.New("permission denied"))
	}
	if s.authorizer == nil {
		return "", valuesRevisionError(connect.CodeFailedPrecondition, "authorization_snapshot_stale",
			errors.New("authorization_snapshot_stale"))
	}
	if err := s.authorizer.AuthorizeWrite(ctx, actor, definition.CustomerID, store.AuthorizationCreateValues); err != nil {
		return "", valuesAuthorizationError(err)
	}
	return member.Role, nil
}

func (s *Service) authorizeValuesRead(ctx context.Context, definitionID string) error {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return valuesRevisionError(connect.CodeUnauthenticated, "authentication_required", errors.New("authentication required"))
	}
	definition, err := s.store.Definitions().Get(ctx, definitionID)
	if errors.Is(err, store.ErrNotFound) {
		return valuesRevisionError(connect.CodeNotFound, "release_definition_not_found", errors.New("release_definition_not_found"))
	}
	if err != nil {
		return s.stableInternalError("get release definition", err)
	}
	if definition.OwnerOrganizationID != nil && *definition.OwnerOrganizationID != actor.OrganizationID {
		return valuesRevisionError(connect.CodePermissionDenied, "permission_denied", errors.New("permission denied"))
	}
	if err := s.store.Bindings().RequireActive(ctx, actor.OrganizationID, definition.CustomerID); err != nil {
		return valuesRevisionError(connect.CodePermissionDenied, "permission_denied", errors.New("permission denied"))
	}
	if _, err := s.store.OrgMembers().Get(ctx, actor.OrganizationID, actor.UserID); err != nil {
		return valuesRevisionError(connect.CodePermissionDenied, "permission_denied", errors.New("permission denied"))
	}
	return nil
}

func validateValuesIdempotencyKey(key string) error {
	if key == "" || len(key) > 64 {
		return valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument",
			errors.New("Idempotency-Key header must contain 1-64 characters"))
	}
	return nil
}

func valuesValidationConnectError(err error) *connect.Error {
	switch {
	case values.IsYAMLError(err):
		return valuesRevisionError(connect.CodeInvalidArgument, "invalid_yaml", errors.New("invalid_yaml"))
	case errors.Is(err, values.ErrSizeExceeded):
		return valuesRevisionError(connect.CodeResourceExhausted, "size_exceeded", errors.New("size_exceeded"))
	case errors.Is(err, values.ErrSecretLiteral):
		return valuesRevisionError(connect.CodeInvalidArgument, "secret_literal_forbidden", errors.New("secret_literal_forbidden"))
	case errors.Is(err, values.ErrInvalidSecretRef):
		return valuesRevisionError(connect.CodeInvalidArgument, "invalid_secret_ref", errors.New("invalid_secret_ref"))
	default:
		return valuesRevisionError(connect.CodeInvalidArgument, "invalid_yaml", errors.New("invalid_yaml"))
	}
}

func valuesLifecycleConnectError(err error, definitionID string) *connect.Error {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr
	}
	switch {
	case errors.Is(err, store.ErrAuthorizationStale):
		return valuesRevisionError(connect.CodeFailedPrecondition, "authorization_snapshot_stale", errors.New("authorization_snapshot_stale"))
	case errors.Is(err, store.ErrIdempotencyConflict):
		return valuesRevisionError(connect.CodeAlreadyExists, "idempotency_conflict", errors.New("idempotency_conflict"))
	case errors.Is(err, store.ErrDuplicateKey):
		return valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument",
			errors.New("parent_revision_id is required when the definition has revisions"))
	case errors.Is(err, store.ErrParentConflict):
		return valuesRevisionError(connect.CodeAborted, "parent_conflict",
			fmt.Errorf("parent_conflict: release_definition=%s", definitionID))
	case errors.Is(err, store.ErrPrepareTokenExpired):
		return valuesRevisionError(connect.CodeFailedPrecondition, "prepare_token_expired", errors.New("prepare_token_expired"))
	case errors.Is(err, store.ErrPrepareTokenConsumed):
		return valuesRevisionError(connect.CodeAlreadyExists, "prepare_token_consumed", errors.New("prepare_token_consumed"))
	case errors.Is(err, store.ErrConvergenceRevisionExists):
		return valuesRevisionError(connect.CodeAlreadyExists, "convergence_revision_exists", errors.New("convergence_revision_exists"))
	case errors.Is(err, store.ErrConvergenceConflict):
		return valuesRevisionError(connect.CodeFailedPrecondition, "convergence_conflict", errors.New("convergence_conflict"))
	case errors.Is(err, store.ErrNotFound):
		return valuesRevisionError(connect.CodeNotFound, "release_definition_not_found", errors.New("release_definition_not_found"))
	default:
		return valuesRevisionError(connect.CodeInternal, "internal_error", errors.New("create values draft failed"))
	}
}

func discardConnectError(err error, revisionID string) *connect.Error {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr
	}
	switch {
	case errors.Is(err, store.ErrAuthorizationStale):
		return valuesRevisionError(connect.CodeFailedPrecondition, "authorization_snapshot_stale", errors.New("authorization_snapshot_stale"))
	case errors.Is(err, store.ErrIdempotencyConflict):
		return valuesRevisionError(connect.CodeAlreadyExists, "idempotency_conflict", errors.New("idempotency_conflict"))
	case errors.Is(err, store.ErrDiscardNotAllowed), errors.Is(err, store.ErrOptimisticLock):
		return valuesRevisionError(connect.CodeFailedPrecondition, "discard_not_allowed",
			fmt.Errorf("discard_not_allowed: revision=%s", revisionID))
	case errors.Is(err, store.ErrNotFound):
		return valuesRevisionError(connect.CodeNotFound, "revision_not_found", errors.New("revision_not_found"))
	default:
		return valuesRevisionError(connect.CodeInternal, "internal_error", errors.New("discard values revision failed"))
	}
}

func valuesRevisionError(code connect.Code, reason string, err error) *connect.Error {
	connectErr := connect.NewError(code, err)
	connectErr.Meta().Set("X-Reason-Code", reason)
	return connectErr
}

// stableInternalError returns a client-safe internal error and logs the
// underlying cause server-side. REQ-010: never expose stack, SQL, or driver
// text to clients.
func (s *Service) stableInternalError(operation string, cause error) *connect.Error {
	s.logger.Warn("values_revision_internal_error", "operation", operation, "error", cause)
	return valuesRevisionError(connect.CodeInternal, "internal_error", errors.New("internal error"))
}

func valuesAuthorizationError(err error) *connect.Error {
	if connect.CodeOf(err) == connect.CodePermissionDenied {
		return valuesRevisionError(connect.CodePermissionDenied, "permission_denied", errors.New("permission denied"))
	}
	return valuesRevisionError(connect.CodeFailedPrecondition, "authorization_snapshot_stale", errors.New("authorization_snapshot_stale"))
}

func normalizedValuesComment(comment string) (*string, error) {
	trimmed := strings.TrimSpace(comment)
	if trimmed == "" {
		return nil, nil
	}
	if !utf8.ValidString(trimmed) {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("comment is invalid UTF-8"))
	}
	if len([]byte(trimmed)) > maxApprovalTextBytes {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("comment too large"))
	}
	for _, char := range trimmed {
		if unicode.IsControl(char) {
			return nil, valuesRevisionError(connect.CodeInvalidArgument, "invalid_argument", errors.New("comment contains control characters"))
		}
	}
	return &trimmed, nil
}
func hashPrepareToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func hashCreateRequest(msg *orchestratorv1.CreateValuesRevisionRequest, refs []store.SecretRef) string {
	digest := sha256.New()
	writeHashString(digest, msg.GetReleaseDefinitionId())
	writeHashString(digest, msg.GetDocument())
	for _, ref := range refs {
		writeHashString(digest, ref.Path)
		writeHashString(digest, ref.Name)
		writeHashString(digest, ref.Key)
	}
	writeHashString(digest, msg.GetParentRevisionId())
	writeHashInt64(digest, msg.GetExpectedParentVersion())
	writeHashString(digest, msg.GetPrepareToken())
	return hex.EncodeToString(digest.Sum(nil))
}

func hashDiscardRequest(msg *orchestratorv1.DiscardValuesRevisionRequest) string {
	digest := sha256.New()
	writeHashString(digest, msg.GetRevisionId())
	writeHashInt64(digest, msg.GetExpectedStateVersion())
	writeHashString(digest, msg.GetComment())
	return hex.EncodeToString(digest.Sum(nil))
}

func writeHashString(digest hash.Hash, value string) {
	writeHashUint64(digest, uint64(len(value)))
	_, _ = digest.Write([]byte(value))
}

func writeHashInt64(digest hash.Hash, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value)) //nolint:gosec // Two's-complement encoding preserves the signed request value exactly.
	_, _ = digest.Write(encoded[:])
}

func writeHashUint64(digest hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = digest.Write(encoded[:])
}

func idempotencyCreateScope(actorUserID, definitionID string) string {
	return fmt.Sprintf("create-values:%s:%s", actorUserID, definitionID)
}

func idempotencyDiscardScope(actorUserID, revisionID string) string {
	return fmt.Sprintf("discard-values:%s:%s", actorUserID, revisionID)
}

func prepareTokenHash(session *store.PrepareSession) string {
	if session == nil {
		return ""
	}
	return session.TokenHash
}

func prepareLockedPathHash(session *store.PrepareSession) string {
	if session == nil {
		return ""
	}
	return session.LockedPathHash
}

func protoStatusToStore(status commonv1.ValuesStatus) store.ValuesStatus {
	switch status {
	case commonv1.ValuesStatus_VALUES_STATUS_DRAFT:
		return store.ValuesStatusDraft
	case commonv1.ValuesStatus_VALUES_STATUS_PENDING_APPROVAL:
		return store.ValuesStatusPendingApproval
	case commonv1.ValuesStatus_VALUES_STATUS_APPROVED:
		return store.ValuesStatusApproved
	case commonv1.ValuesStatus_VALUES_STATUS_REJECTED:
		return store.ValuesStatusRejected
	case commonv1.ValuesStatus_VALUES_STATUS_SUPERSEDED:
		return store.ValuesStatusSuperseded
	case commonv1.ValuesStatus_VALUES_STATUS_DISCARDED:
		return store.ValuesStatusDiscarded
	default:
		return ""
	}
}

func toValuesDiscardDecisionResponse(result *store.DiscardValuesResult) *orchestratorv1.ValuesRevisionDecisionResponse {
	return &orchestratorv1.ValuesRevisionDecisionResponse{
		Revision:              toProtoValuesRevision(result.Revision),
		PreviousState:         valuesStatusToProto(result.PreviousState),
		NewState:              valuesStatusToProto(result.NewState),
		DecidedAt:             timestamppb.New(result.DecidedAt),
		SupersededRevisionIds: []string{},
	}
}

type secretMetadataCommandPayload struct {
	DefinitionID string `json:"definition_id"`
	Namespace    string `json:"namespace"`
}

type secretMetadataCommandResult struct {
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Secrets []struct {
		Name string   `json:"name"`
		Keys []string `json:"keys"`
	} `json:"secrets,omitempty"`
}

// requestSecretMetadata dispatches a durable operator command and polls its persisted result.
//nolint:gocyclo // Independent offline, result, and timeout branches make the control flow explicit.
func (s *Service) requestSecretMetadata(ctx context.Context, definition *store.ReleaseDefinition) ([]*orchestratorv1.SecretOption, error) {
	operator, err := s.store.Operators().GetByClusterID(ctx, definition.ClusterID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, valuesRevisionError(connect.CodeUnavailable, "operator_offline", errors.New("operator is offline"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get secret metadata operator: %w", err))
	}
	session, err := s.store.Sessions().GetActiveByOperator(ctx, operator.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && session.Status != store.SessionOnline) {
		return nil, valuesRevisionError(connect.CodeUnavailable, "operator_offline", errors.New("operator is offline"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get secret metadata operator session: %w", err))
	}

	payload, err := json.Marshal(secretMetadataCommandPayload{DefinitionID: definition.ID, Namespace: definition.Namespace})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode secret metadata command: %w", err))
	}
	commandID := uuid.NewString()
	outboxID := uuid.NewString()
	if err := s.store.Outbox().Create(ctx, &store.OutboxEntry{
		ID: outboxID, CommandID: commandID, OperationID: uuid.NewString(), OperationType: commandtype.SecretMetadataList,
		OperatorID: operator.ID, Payload: payload, MaxInFlight: 1,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create secret metadata command: %w", err))
	}

	waitCtx, cancel := context.WithTimeout(ctx, secretMetadataTimeout)
	defer cancel()
	ticker := time.NewTicker(secretMetadataPollInterval)
	defer ticker.Stop()
	for {
		entry, getErr := s.store.Outbox().GetByCommandID(waitCtx, commandID)
		if getErr == nil && (entry.Status == store.CommandSucceeded || entry.Status == store.CommandFailed) {
			var result secretMetadataCommandResult
			if err := json.Unmarshal([]byte(entry.ResultJSON), &result); err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode secret metadata result: %w", err))
			}
			if entry.Status == store.CommandFailed || result.Status != "succeeded" {
				return nil, valuesRevisionError(connect.CodeUnavailable, "secret_metadata_unavailable", errors.New("secret metadata is temporarily unavailable"))
			}
			options := make([]*orchestratorv1.SecretOption, 0, len(result.Secrets))
			for _, secret := range result.Secrets {
				keys := append([]string(nil), secret.Keys...)
				sort.Strings(keys)
				options = append(options, &orchestratorv1.SecretOption{Name: secret.Name, Keys: keys})
			}
			sort.Slice(options, func(i, j int) bool { return options[i].GetName() < options[j].GetName() })
			return options, nil
		}
		if getErr != nil && !errors.Is(getErr, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("poll secret metadata command: %w", getErr))
		}
		select {
		case <-waitCtx.Done():
			return nil, valuesRevisionError(connect.CodeUnavailable, "operator_timeout", errors.New("operator did not return secret metadata in time"))
		case <-ticker.C:
		}
	}
}

// ListSecrets returns available Kubernetes Secret metadata for the release definition's namespace.
func (s *Service) ListSecrets(ctx context.Context, req *connect.Request[orchestratorv1.ListSecretsRequest]) (*connect.Response[orchestratorv1.ListSecretsResponse], error) {
	msg := req.Msg
	if msg.GetClusterId() == "" || msg.GetReleaseDefinitionId() == "" {
		return nil, valuesRevisionError(connect.CodeInvalidArgument, "scope_required", errors.New("cluster_id and release_definition_id are required"))
	}
	definition, err := s.store.Definitions().Get(ctx, msg.GetReleaseDefinitionId())
	if errors.Is(err, store.ErrNotFound) || (err == nil && definition.ClusterID != msg.GetClusterId()) {
		return nil, valuesRevisionError(connect.CodeNotFound, "release_definition_not_found", errors.New("release definition not found"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get secret metadata release definition: %w", err))
	}
	if err := s.authorizeValuesRead(ctx, definition.ID); err != nil {
		return nil, err
	}
	secrets, err := s.requestSecretMetadata(ctx, definition)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&orchestratorv1.ListSecretsResponse{Secrets: secrets}), nil
}
