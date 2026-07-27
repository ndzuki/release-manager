package orchestrator

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// CreateEnrollmentToken creates or explicitly replaces one pending hash-only token.
//
//nolint:gocyclo // Enrollment creation keeps validation, audit, and atomic replacement gates explicit.
func (s *Service) CreateEnrollmentToken(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateEnrollmentTokenRequest],
) (*connect.Response[orchestratorv1.CreateEnrollmentTokenResponse], error) {
	msg := req.Msg
	if err := s.validateOperatorScope(ctx, msg.GetCustomerId(), msg.GetClusterId()); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(msg.GetOperatorName())
	if err := validateOperatorName(name); err != nil {
		return nil, err
	}
	ttlMinutes := msg.GetTtlMinutes()
	if ttlMinutes == 0 {
		ttlMinutes = defaultEnrollmentTTLMinutes
	}
	if ttlMinutes < minimumEnrollmentTTLMinutes || ttlMinutes > maximumEnrollmentTTLMinutes {
		return nil, operatorError(connect.CodeInvalidArgument, "invalid_argument", "ttl_minutes must be 0 or between 5 and 1440", "ttl_minutes")
	}
	if existing, err := s.store.Operators().GetActiveByName(ctx, msg.GetCustomerId(), name); err == nil && existing.Status == store.OperatorActive {
		return nil, operatorError(connect.CodeAlreadyExists, "duplicate_operator_name", "an active operator with this name already exists", "operator_name")
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, operatorError(connect.CodeInternal, "internal", "unable to validate operator name", "")
	}

	plaintext, err := generateEnrollmentToken()
	if err != nil {
		return nil, operatorError(connect.CodeInternal, "internal", "unable to generate enrollment token", "")
	}
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, operatorError(connect.CodeUnauthenticated, "permission_denied", "authentication required", "")
	}
	user, err := s.store.Users().Get(ctx, actor.UserID)
	if err != nil {
		return nil, operatorError(connect.CodeUnavailable, "audit_unavailable", "audit identity is unavailable", "")
	}
	now := time.Now().UTC()
	token := &store.EnrollmentToken{
		ID:                   uuid.NewString(),
		CustomerID:           msg.GetCustomerId(),
		ClusterID:            msg.GetClusterId(),
		OperatorName:         name,
		Token:                plaintext,
		State:                store.TokenStatePending,
		CreatedByDisplayName: user.Username,
		CreatedAt:            now,
		ExpiresAt:            now.Add(time.Duration(ttlMinutes) * time.Minute),
	}
	action := "operator.enrollment_token.created"
	if msg.GetReplacePendingToken() {
		action = "operator.enrollment_token.replaced"
	}
	auditEvent, err := s.operatorAuditEvent(ctx, "enrollment_token", token.ID, action, map[string]string{
		"customer_id":   token.CustomerID,
		"cluster_id":    token.ClusterID,
		"operator_name": token.OperatorName,
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.store.OperatorManagement().CreateEnrollmentToken(ctx, token, msg.GetReplacePendingToken(), auditEvent); err != nil {
		return nil, mapOperatorStoreError(err)
	}
	cluster, err := s.store.Clusters().Get(ctx, token.ClusterID)
	if err != nil {
		return nil, operatorError(connect.CodeInternal, "internal", "unable to read cluster", "")
	}
	return connect.NewResponse(&orchestratorv1.CreateEnrollmentTokenResponse{
		Token:                         plaintext,
		ExpiresAt:                     timestamppb.New(token.ExpiresAt),
		CustomerId:                    token.CustomerID,
		ClusterId:                     token.ClusterID,
		ClusterName:                   cluster.Name,
		OperatorEndpoint:              s.operatorEndpoint,
		InstallCommandTemplateVersion: installTemplateVersion,
		InstallCommandTemplate: strings.NewReplacer(
			"http://operator:8084", s.operatorEndpoint,
			"${CUSTOMER_ID}", token.CustomerID,
			"${CLUSTER_ID}", token.ClusterID,
		).Replace(installTemplate),
	}), nil
}

func validateOperatorName(name string) error {
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 63 {
		return operatorError(connect.CodeInvalidArgument, "invalid_argument", "operator_name must contain 1 to 63 characters", "operator_name")
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return operatorError(connect.CodeInvalidArgument, "invalid_argument", "operator_name must be a DNS-compatible label", "operator_name")
	}
	for _, value := range name {
		if (value < 'a' || value > 'z') && (value < '0' || value > '9') && value != '-' {
			return operatorError(connect.CodeInvalidArgument, "invalid_argument", "operator_name must be a DNS-compatible label", "operator_name")
		}
	}
	return nil
}
