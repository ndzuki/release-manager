package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/operator/commandtype"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/values"
)

const (
	secretMetadataTimeout      = 15 * time.Second
	secretMetadataPollInterval = 50 * time.Millisecond
)

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

func valuesReasonError(code connect.Code, reason, message string) *connect.Error {
	err := connect.NewError(code, errors.New(message))
	err.Meta().Set("X-Reason-Code", reason)
	return err
}

func (s *Service) requireDefinitionAccess(ctx context.Context, definition *store.ReleaseDefinition) error {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok || actor.OrganizationID == "" {
		return valuesReasonError(connect.CodeUnauthenticated, "authentication_required", "authenticated organization is required")
	}
	if err := s.store.Bindings().RequireActive(ctx, actor.OrganizationID, definition.CustomerID); err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrBindingRevoked) {
			return valuesReasonError(connect.CodePermissionDenied, "permission_denied", "organization cannot access the release definition")
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("check release definition access: %w", err))
	}
	return nil
}

// requestSecretMetadata dispatches a durable operator command and polls its persisted result.
//nolint:gocyclo // Independent offline, result, and timeout branches make the control flow explicit.
func (s *Service) requestSecretMetadata(ctx context.Context, definition *store.ReleaseDefinition) ([]*orchestratorv1.SecretOption, error) {
	operator, err := s.store.Operators().GetByClusterID(ctx, definition.ClusterID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, valuesReasonError(connect.CodeUnavailable, "operator_offline", "operator is offline")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get secret metadata operator: %w", err))
	}
	session, err := s.store.Sessions().GetActiveByOperator(ctx, operator.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && session.Status != store.SessionOnline) {
		return nil, valuesReasonError(connect.CodeUnavailable, "operator_offline", "operator is offline")
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
				return nil, valuesReasonError(connect.CodeUnavailable, "secret_metadata_unavailable", "secret metadata is temporarily unavailable")
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
			return nil, valuesReasonError(connect.CodeUnavailable, "operator_timeout", "operator did not return secret metadata in time")
		case <-ticker.C:
		}
	}
}

func valuesConnectError(err error) error {
	switch {
	case errors.Is(err, values.ErrSecretLiteral):
		return valuesReasonError(connect.CodeInvalidArgument, "secret_literal_forbidden", "use SecretRef instead of literal secret data")
	case errors.Is(err, values.ErrSizeExceeded):
		return valuesReasonError(connect.CodeInvalidArgument, "size_exceeded", "document exceeds the configured limit")
	case values.IsYAMLError(err):
		return valuesReasonError(connect.CodeInvalidArgument, "invalid_yaml", err.Error())
	default:
		return valuesReasonError(connect.CodeInvalidArgument, "invalid_yaml", err.Error())
	}
}

// CreateValuesRevision creates a new draft values revision.
func (s *Service) CreateValuesRevision(ctx context.Context, req *connect.Request[orchestratorv1.CreateValuesRevisionRequest]) (*connect.Response[orchestratorv1.CreateValuesRevisionResponse], error) {
	msg := req.Msg
	if msg.GetReleaseDefinitionId() == "" {
		return nil, valuesReasonError(connect.CodeInvalidArgument, "release_definition_id_required", "release_definition_id is required")
	}
	if len(msg.GetDocument()) > 1<<20 {
		return nil, valuesReasonError(connect.CodeInvalidArgument, "size_exceeded", "document exceeds 1 MiB")
	}
	for index, ref := range msg.GetSecretRefs() {
		if ref.GetName() == "" || ref.GetKey() == "" {
			return nil, valuesReasonError(connect.CodeInvalidArgument, "invalid_secret_ref", fmt.Sprintf("secret_refs[%d] requires path, name, and key", index))
		}
	}
	definition, err := s.store.Definitions().Get(ctx, msg.GetReleaseDefinitionId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, valuesReasonError(connect.CodeNotFound, "release_definition_not_found", "release definition not found")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get release definition: %w", err))
	}
	if err := s.requireDefinitionAccess(ctx, definition); err != nil {
		return nil, err
	}
	canonical, err := values.Validate(msg.GetDocument(), 1<<20)
	if err != nil {
		return nil, valuesConnectError(err)
	}
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok || actor.UserID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authenticated user is required"))
	}
	secretRefs, err := json.Marshal(msg.GetSecretRefs())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal secret references: %w", err))
	}
	revision := &store.ValuesRevision{
		ID:                  uuid.NewString(),
		ReleaseDefinitionID: definition.ID,
		Status:              store.ValuesStatusDraft,
		Values:              canonical.Canonical,
		Digest:              "sha256:" + canonical.Digest,
		ParentRevisionID:    msg.GetParentRevisionId(),
		SecretRefs:          secretRefs,
		CreatedByUserID:     actor.UserID,
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}
	if err := s.store.Values().Create(ctx, revision); err != nil {
		if errors.Is(err, store.ErrDuplicateKey) {
			return nil, valuesReasonError(connect.CodeAlreadyExists, "revision_exists", "a revision already exists")
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create values revision: %w", err))
	}
	return connect.NewResponse(&orchestratorv1.CreateValuesRevisionResponse{Revision: toProtoValuesRevision(revision)}), nil
}

// GetValuesRevision returns a single values revision by ID.
func (s *Service) GetValuesRevision(ctx context.Context, req *connect.Request[orchestratorv1.GetValuesRevisionRequest]) (*connect.Response[orchestratorv1.GetValuesRevisionResponse], error) {
	if req.Msg.GetRevisionId() == "" {
		return nil, valuesReasonError(connect.CodeInvalidArgument, "revision_id_required", "revision_id is required")
	}
	revision, err := s.store.Values().Get(ctx, req.Msg.GetRevisionId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, valuesReasonError(connect.CodeNotFound, "revision_not_found", "revision not found")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get values revision: %w", err))
	}
	definition, err := s.store.Definitions().Get(ctx, revision.ReleaseDefinitionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get values revision definition: %w", err))
	}
	if err := s.requireDefinitionAccess(ctx, definition); err != nil {
		return nil, err
	}
	return connect.NewResponse(&orchestratorv1.GetValuesRevisionResponse{Revision: toProtoValuesRevision(revision)}), nil
}

// ListValuesRevisions returns revisions for a release definition.
func (s *Service) ListValuesRevisions(ctx context.Context, req *connect.Request[orchestratorv1.ListValuesRevisionsRequest]) (*connect.Response[orchestratorv1.ListValuesRevisionsResponse], error) {
	if req.Msg.GetReleaseDefinitionId() == "" {
		return nil, valuesReasonError(connect.CodeInvalidArgument, "release_definition_id_required", "release_definition_id is required")
	}
	definition, err := s.store.Definitions().Get(ctx, req.Msg.GetReleaseDefinitionId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, valuesReasonError(connect.CodeNotFound, "release_definition_not_found", "release definition not found")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get values revision definition: %w", err))
	}
	if err := s.requireDefinitionAccess(ctx, definition); err != nil {
		return nil, err
	}
	revisions, err := s.store.Values().List(ctx, definition.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list values revisions: %w", err))
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = 50
	} else if limit > 50 {
		limit = 50
	}
	result := make([]*commonv1.ValuesRevision, 0, min(limit, len(revisions)))
	for _, revision := range revisions {
		result = append(result, toProtoValuesRevision(revision))
		if len(result) == limit {
			break
		}
	}
	return connect.NewResponse(&orchestratorv1.ListValuesRevisionsResponse{Revisions: result}), nil
}

// ListSecrets returns available Kubernetes Secret metadata for the release definition's namespace.
func (s *Service) ListSecrets(ctx context.Context, req *connect.Request[orchestratorv1.ListSecretsRequest]) (*connect.Response[orchestratorv1.ListSecretsResponse], error) {
	msg := req.Msg
	if msg.GetClusterId() == "" || msg.GetReleaseDefinitionId() == "" {
		return nil, valuesReasonError(connect.CodeInvalidArgument, "scope_required", "cluster_id and release_definition_id are required")
	}
	definition, err := s.store.Definitions().Get(ctx, msg.GetReleaseDefinitionId())
	if errors.Is(err, store.ErrNotFound) || (err == nil && definition.ClusterID != msg.GetClusterId()) {
		return nil, valuesReasonError(connect.CodeNotFound, "release_definition_not_found", "release definition not found")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get secret metadata release definition: %w", err))
	}
	if err := s.requireDefinitionAccess(ctx, definition); err != nil {
		return nil, err
	}
	secrets, err := s.requestSecretMetadata(ctx, definition)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&orchestratorv1.ListSecretsResponse{Secrets: secrets}), nil
}
