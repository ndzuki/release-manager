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
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/operator/commandtype"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/values"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	organizationID, ok := auth.OrganizationIDFromContext(ctx)
	if !ok || organizationID == "" {
		return valuesReasonError(connect.CodeUnauthenticated, "authentication_required", "authenticated organization is required")
	}
	if err := s.store.Bindings().RequireActive(ctx, organizationID, definition.CustomerID); err != nil {
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

func valuesStatusToProto(status store.ValuesStatus) commonv1.ValuesStatus {
	switch status {
	case store.ValuesStatusDraft:
		return commonv1.ValuesStatus_VALUES_STATUS_DRAFT
	case store.ValuesStatusApproved:
		return commonv1.ValuesStatus_VALUES_STATUS_APPROVED
	case store.ValuesStatusRejected:
		return commonv1.ValuesStatus_VALUES_STATUS_REJECTED
	case store.ValuesStatusSuperseded:
		return commonv1.ValuesStatus_VALUES_STATUS_SUPERSEDED
	default:
		return commonv1.ValuesStatus_VALUES_STATUS_UNSPECIFIED
	}
}

func toProtoValuesRevision(revision *store.ValuesRevision) *commonv1.ValuesRevision {
	if revision == nil {
		return nil
	}
	message := &commonv1.ValuesRevision{
		Id:                  revision.ID,
		ReleaseDefinitionId: revision.ReleaseDefinitionID,
		Revision:            int32(revision.Revision), //nolint:gosec // Persisted revision numbers are positive and bounded by SQLite INTEGER usage.

		Values:           append([]byte(nil), revision.Values...),
		CreatedAt:        timestamppb.New(revision.CreatedAt),
		Status:           valuesStatusToProto(revision.Status),
		Digest:           revision.Digest,
		ParentRevisionId: revision.ParentRevisionID,
		Version:          int32(revision.Version), //nolint:gosec // Optimistic versions are positive and bounded by persisted revisions.

		CreatedBy:  revision.CreatedBy,
		ApprovedBy: revision.ApprovedBy,
		RejectedBy: revision.RejectedBy,
		Reason:     revision.RejectionReason,
	}
	if revision.ApprovedAt != nil {
		message.ApprovedAt = timestamppb.New(*revision.ApprovedAt)
	}
	if revision.RejectedAt != nil {
		message.RejectedAt = timestamppb.New(*revision.RejectedAt)
	}
	if len(revision.SecretRefs) > 0 {
		// Invalid persisted metadata is omitted rather than exposing malformed references.
		if refs, err := decodeSecretRefs(revision.SecretRefs); err == nil {
			message.SecretRefs = refs
		}
	}

	return message
}

func decodeSecretRefs(data []byte) ([]*commonv1.SecretRef, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var refs []*commonv1.SecretRef
	if err := json.Unmarshal(data, &refs); err != nil {
		return nil, err
	}
	return refs, nil
}

func canApprove(ctx context.Context, st store.Store, releaseDefinitionID string) bool {
	userID, userOK := auth.UserIDFromContext(ctx)
	organizationID, organizationOK := auth.OrganizationIDFromContext(ctx)
	if !userOK || !organizationOK || userID == "" || organizationID == "" {
		return false
	}
	definition, err := st.Definitions().Get(ctx, releaseDefinitionID)
	if err != nil || st.Bindings().RequireActive(ctx, organizationID, definition.CustomerID) != nil {
		return false
	}
	member, err := st.OrgMembers().Get(ctx, organizationID, userID)
	if err != nil {
		return false
	}
	return member.Role == store.RoleReleaseAdmin || member.Role == store.RolePlatformAdmin
}

func (s *Service) emitValuesAudit(ctx context.Context, revision *store.ValuesRevision, action, summary string) {
	userID, _ := auth.UserIDFromContext(ctx)
	organizationID, _ := auth.OrganizationIDFromContext(ctx)
	role := ""
	if member, err := s.store.OrgMembers().Get(ctx, organizationID, userID); err == nil {
		role = string(member.Role)
	}
	s.emitAudit(audit.NewEvent(
		store.AuditActorUser, userID, organizationID, role,
		"values_revision", revision.ID, action, "succeeded", summary, nil,
	))
}
