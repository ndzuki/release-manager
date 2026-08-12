package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

const (
	defaultOperatorPageSize          int32 = 20
	maximumOperatorPageSize          int32 = 100
	defaultEnrollmentTTLMinutes      int32 = 60
	minimumEnrollmentTTLMinutes      int32 = 5
	maximumEnrollmentTTLMinutes      int32 = 1440
	operatorHeartbeatIntervalSeconds       = 30
	installTemplateVersion                 = "v1"
	installTemplate                        = "release-operator --orchestrator-addr http://orchestrator:8083 --operator-addr http://operator:8084 --customer-id ${CUSTOMER_ID} --cluster-id ${CLUSTER_ID} --enrollment-token ${ENROLLMENT_TOKEN}"
)

type operatorCursorPayload struct {
	Version         int    `json:"v"`
	CustomerID      string `json:"customer_id"`
	ClusterID       string `json:"cluster_id"`
	LifecycleStatus string `json:"lifecycle_status,omitempty"`
	SessionStatus   string `json:"session_status,omitempty"`
	RegisteredAt    string `json:"registered_at"`
	OperatorID      string `json:"operator_id"`
}

// ListOperators returns a stable cursor-paginated Operator history for one Cluster.
func (s *Service) ListOperators(
	ctx context.Context,
	req *connect.Request[orchestratorv1.ListOperatorsRequest],
) (*connect.Response[orchestratorv1.ListOperatorsResponse], error) {
	msg := req.Msg
	if err := s.validateOperatorScope(ctx, msg.GetCustomerId(), msg.GetClusterId()); err != nil {
		return nil, err
	}
	pageSize := msg.GetPageSize()
	if pageSize == 0 {
		pageSize = defaultOperatorPageSize
	}
	if pageSize < 0 || pageSize > maximumOperatorPageSize {
		return nil, operatorError(connect.CodeInvalidArgument, "invalid_argument", "page_size must be between 1 and 100", "page_size")
	}
	filter, err := operatorFilterFromProto(msg)
	if err != nil {
		return nil, err
	}
	cursor, err := decodeOperatorCursor(msg.GetPageToken(), msg.GetCustomerId(), msg.GetClusterId(), filter)
	if err != nil {
		return nil, err
	}
	page, err := s.store.Operators().ListByClusterFilter(ctx, msg.GetCustomerId(), msg.GetClusterId(), filter, pageSize, cursor)
	if err != nil {
		return nil, operatorError(connect.CodeInternal, "internal", "unable to list operators", "")
	}
	operators := make([]*orchestratorv1.OperatorSummary, 0, len(page.Operators))
	for _, operator := range page.Operators {
		summary, summaryErr := s.operatorSummary(ctx, operator, nil)
		if summaryErr != nil {
			return nil, summaryErr
		}
		operators = append(operators, summary)
	}
	return connect.NewResponse(&orchestratorv1.ListOperatorsResponse{
		Operators:                operators,
		NextPageToken:            encodeOperatorCursor(page.NextPageCursor),
		TotalCount:               page.TotalCount,
		HeartbeatIntervalSeconds: operatorHeartbeatIntervalSeconds,
	}), nil
}

// GetOperator returns the authoritative Operator lifecycle and latest Session state.
func (s *Service) GetOperator(
	ctx context.Context,
	req *connect.Request[orchestratorv1.GetOperatorRequest],
) (*connect.Response[orchestratorv1.GetOperatorResponse], error) {
	msg := req.Msg
	if err := s.validateOperatorScope(ctx, msg.GetCustomerId(), msg.GetClusterId()); err != nil {
		return nil, err
	}
	operator, err := s.scopedOperator(ctx, msg.GetCustomerId(), msg.GetClusterId(), msg.GetOperatorId())
	if err != nil {
		return nil, err
	}
	session, err := s.latestOperatorSession(ctx, operator.ID)
	if err != nil {
		return nil, err
	}
	summary, err := s.operatorSummary(ctx, operator, session)
	if err != nil {
		return nil, err
	}
	detail := &orchestratorv1.OperatorDetail{Summary: summary, SupersededBy: operator.SupersededBy, RevokeReason: operator.RevokeReason}
	if session != nil {
		detail.InstanceId = session.InstanceID
		detail.Version = session.Version
		detail.Capabilities = session.Capabilities
	}
	return connect.NewResponse(&orchestratorv1.GetOperatorResponse{
		Operator:                 detail,
		HeartbeatIntervalSeconds: operatorHeartbeatIntervalSeconds,
	}), nil
}

// RevokeOperator atomically revokes the Operator and Sessions, then closes the active stream.
func (s *Service) RevokeOperator(
	ctx context.Context,
	req *connect.Request[orchestratorv1.RevokeOperatorRequest],
) (*connect.Response[orchestratorv1.RevokeOperatorResponse], error) {
	msg := req.Msg
	if err := s.validateOperatorScope(ctx, msg.GetCustomerId(), msg.GetClusterId()); err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(msg.GetReason())
	if length := utf8.RuneCountInString(reason); length < 5 || length > 500 {
		return nil, operatorError(connect.CodeInvalidArgument, "invalid_argument", "reason must contain 5 to 500 characters", "reason")
	}
	operator, err := s.scopedOperator(ctx, msg.GetCustomerId(), msg.GetClusterId(), msg.GetOperatorId())
	if err != nil {
		return nil, err
	}
	auditEvent, err := s.operatorAuditEvent(ctx, "operator", operator.ID, "operator.revoked", map[string]string{
		"customer_id":    msg.GetCustomerId(),
		"cluster_id":     msg.GetClusterId(),
		"reason_present": "true",
		"reason_length":  fmt.Sprintf("%d", utf8.RuneCountInString(reason)),
	})
	if err != nil {
		return nil, err
	}
	result, err := s.store.OperatorManagement().RevokeOperator(ctx, msg.GetCustomerId(), msg.GetClusterId(), operator.ID, reason, auditEvent)
	if err != nil {
		return nil, mapOperatorStoreError(err)
	}
	if s.streamRevoker != nil {
		if err := s.streamRevoker.Revoke(ctx, operator.ID, "operator revoked"); err != nil {
			return nil, operatorError(connect.CodeUnavailable, "stream_revoke_unavailable", "operator revoked but the active stream could not be closed", "")
		}
	}
	summary, err := s.operatorSummary(ctx, result.Operator, result.Session)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&orchestratorv1.RevokeOperatorResponse{Operator: summary, Changed: result.Changed}), nil
}

// GetEnrollmentTokenStatus returns only non-sensitive pending-token metadata.
func (s *Service) GetEnrollmentTokenStatus(
	ctx context.Context,
	req *connect.Request[orchestratorv1.GetEnrollmentTokenStatusRequest],
) (*connect.Response[orchestratorv1.GetEnrollmentTokenStatusResponse], error) {
	msg := req.Msg
	if err := s.validateOperatorScope(ctx, msg.GetCustomerId(), msg.GetClusterId()); err != nil {
		return nil, err
	}
	token, err := s.store.EnrollmentTokens().GetPendingByCluster(ctx, msg.GetCustomerId(), msg.GetClusterId())
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewResponse(&orchestratorv1.GetEnrollmentTokenStatusResponse{
			Status: &orchestratorv1.EnrollmentTokenStatus{State: orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_UNSPECIFIED},
		}), nil
	}
	if err != nil {
		return nil, operatorError(connect.CodeInternal, "internal", "unable to read enrollment token status", "")
	}
	if time.Now().UTC().After(token.ExpiresAt) {
		return connect.NewResponse(&orchestratorv1.GetEnrollmentTokenStatusResponse{
			Status: &orchestratorv1.EnrollmentTokenStatus{State: orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_UNSPECIFIED},
		}), nil
	}
	return connect.NewResponse(&orchestratorv1.GetEnrollmentTokenStatusResponse{
		Status: &orchestratorv1.EnrollmentTokenStatus{
			State:                orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_PENDING,
			CreatedAt:            timestamppb.New(token.CreatedAt),
			ExpiresAt:            timestamppb.New(token.ExpiresAt),
			CreatedByDisplayName: token.CreatedByDisplayName,
		},
	}), nil
}

// RevokePendingEnrollmentToken idempotently revokes the pending token.
func (s *Service) RevokePendingEnrollmentToken(
	ctx context.Context,
	req *connect.Request[orchestratorv1.RevokePendingEnrollmentTokenRequest],
) (*connect.Response[orchestratorv1.RevokePendingEnrollmentTokenResponse], error) {
	msg := req.Msg
	if err := s.validateOperatorScope(ctx, msg.GetCustomerId(), msg.GetClusterId()); err != nil {
		return nil, err
	}
	auditEvent, err := s.operatorAuditEvent(ctx, "enrollment_token", msg.GetClusterId(), "operator.enrollment_token.revoked", map[string]string{
		"customer_id": msg.GetCustomerId(),
		"cluster_id":  msg.GetClusterId(),
	})
	if err != nil {
		return nil, err
	}
	result, err := s.store.OperatorManagement().RevokePendingEnrollmentToken(ctx, msg.GetCustomerId(), msg.GetClusterId(), auditEvent)
	if err != nil {
		return nil, mapOperatorStoreError(err)
	}
	state := orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_UNSPECIFIED
	if result.Token != nil {
		state = enrollmentTokenStateToProto(result.Token.State)
	}
	return connect.NewResponse(&orchestratorv1.RevokePendingEnrollmentTokenResponse{Changed: result.Changed, FinalState: state}), nil
}

func (s *Service) validateOperatorScope(ctx context.Context, customerID, clusterID string) error {
	if strings.TrimSpace(customerID) == "" {
		return operatorError(connect.CodeInvalidArgument, "invalid_argument", "customer_id is required", "customer_id")
	}
	if strings.TrimSpace(clusterID) == "" {
		return operatorError(connect.CodeInvalidArgument, "invalid_argument", "cluster_id is required", "cluster_id")
	}
	customer, err := s.store.Customers().Get(ctx, customerID)
	if errors.Is(err, store.ErrNotFound) {
		return operatorError(connect.CodeNotFound, "customer_not_found", "customer not found", "")
	}
	if err != nil {
		return operatorError(connect.CodeInternal, "internal", "unable to read customer", "")
	}
	if customer.Status == store.CustomerDisabled {
		return operatorError(connect.CodePermissionDenied, "permission_denied", "customer is disabled", "")
	}
	cluster, err := s.store.Clusters().Get(ctx, clusterID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && cluster.CustomerID != customerID) {
		return operatorError(connect.CodeNotFound, "cluster_not_found", "cluster not found", "")
	}
	if err != nil {
		return operatorError(connect.CodeInternal, "internal", "unable to read cluster", "")
	}
	if cluster.Status == store.ClusterDisabled {
		return operatorError(connect.CodePermissionDenied, "permission_denied", "cluster is disabled", "")
	}
	return nil
}

func (s *Service) scopedOperator(ctx context.Context, customerID, clusterID, operatorID string) (*store.Operator, error) {
	if strings.TrimSpace(operatorID) == "" {
		return nil, operatorError(connect.CodeInvalidArgument, "invalid_argument", "operator_id is required", "operator_id")
	}
	operator, err := s.store.Operators().Get(ctx, operatorID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (operator.CustomerID != customerID || operator.ClusterID != clusterID)) {
		return nil, operatorError(connect.CodeNotFound, "operator_not_found", "operator not found", "")
	}
	if err != nil {
		return nil, operatorError(connect.CodeInternal, "internal", "unable to read operator", "")
	}
	return operator, nil
}

func (s *Service) latestOperatorSession(ctx context.Context, operatorID string) (*store.Session, error) {
	session, err := s.store.Sessions().GetLatestByOperator(ctx, operatorID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, operatorError(connect.CodeInternal, "internal", "unable to read operator session", "")
	}
	return session, nil
}

func (s *Service) operatorSummary(ctx context.Context, operator *store.Operator, session *store.Session) (*orchestratorv1.OperatorSummary, error) {
	if operator == nil {
		return nil, operatorError(connect.CodeInternal, "internal", "operator record is missing", "")
	}
	if session == nil {
		var err error
		session, err = s.latestOperatorSession(ctx, operator.ID)
		if err != nil {
			return nil, err
		}
	}
	cluster, err := s.store.Clusters().Get(ctx, operator.ClusterID)
	if err != nil {
		return nil, operatorError(connect.CodeInternal, "internal", "unable to read operator cluster", "")
	}
	summary := &orchestratorv1.OperatorSummary{
		Id:              operator.ID,
		Name:            operator.Name,
		CustomerId:      operator.CustomerID,
		ClusterId:       operator.ClusterID,
		ClusterName:     cluster.Name,
		LifecycleStatus: operatorLifecycleToProto(operator.Status),
		RegisteredAt:    timestamppb.New(operator.RegisteredAt),
	}
	if operator.SupersededAt != nil {
		summary.SupersededAt = timestamppb.New(*operator.SupersededAt)
	}
	if operator.RevokedAt != nil {
		summary.RevokedAt = timestamppb.New(*operator.RevokedAt)
	}
	if session != nil {
		summary.SessionStatus = operatorSessionStatusToProto(session.Status)
		summary.SessionStatusReason = operatorSessionReasonToProto(session.StatusReason)
		if !session.LastHeartbeat.IsZero() {
			summary.LastHeartbeat = timestamppb.New(session.LastHeartbeat)
		}
	} else {
		summary.SessionStatusReason = orchestratorv1.OperatorSessionStatusReason_OPERATOR_SESSION_STATUS_REASON_NO_SESSION
	}
	return summary, nil
}

func (s *Service) operatorAuditEvent(ctx context.Context, resourceType, resourceID, action string, metadata map[string]string) (*store.AuditEvent, error) {
	actor, ok := authctx.ActorFromContext(ctx)
	if !ok {
		return nil, operatorError(connect.CodeUnauthenticated, "permission_denied", "authentication required", "")
	}
	if _, err := s.store.Users().Get(ctx, actor.UserID); err != nil {
		return nil, operatorError(connect.CodeUnavailable, "audit_unavailable", "audit identity is unavailable", "")
	}
	role := ""
	if len(actor.Roles) > 0 {
		role = actor.Roles[0]
	}
	metadataCopy := make(map[string]string, len(metadata)+1)
	for key, value := range metadata {
		metadataCopy[key] = value
	}
	metadataCopy["request_id"] = uuid.NewString()
	return &store.AuditEvent{
		ID:             uuid.NewString(),
		ActorKind:      store.AuditActorUser,
		ActorID:        actor.UserID,
		OrganizationID: actor.OrganizationID,
		Role:           role,
		ResourceType:   resourceType,
		ResourceID:     resourceID,
		Action:         action,
		Status:         "succeeded",
		ChangeSummary:  "operator management action",
		Metadata:       metadataCopy,
		CreatedAt:      time.Now().UTC(),
	}, nil
}

func operatorFilterFromProto(msg *orchestratorv1.ListOperatorsRequest) (store.OperatorListFilter, error) {
	filter := store.OperatorListFilter{}
	if msg.LifecycleStatus != nil {
		value, ok := operatorLifecycleFromProto(*msg.LifecycleStatus)
		if !ok {
			return filter, operatorError(connect.CodeInvalidArgument, "invalid_argument", "invalid lifecycle_status", "lifecycle_status")
		}
		filter.LifecycleStatus = &value
	}
	if msg.SessionStatus != nil {
		if *msg.SessionStatus == orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_UNSPECIFIED {
			filter.NoSession = true
		} else {
			value, ok := operatorSessionStatusFromProto(*msg.SessionStatus)
			if !ok {
				return filter, operatorError(connect.CodeInvalidArgument, "invalid_argument", "invalid session_status", "session_status")
			}
			filter.SessionStatus = &value
		}
	}
	return filter, nil
}

func decodeOperatorCursor(raw, customerID, clusterID string, filter store.OperatorListFilter) (*store.OperatorCursor, error) {
	if raw == "" {
		return nil, nil
	}
	encoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, operatorError(connect.CodeInvalidArgument, "invalid_argument", "invalid page_token", "page_token")
	}
	var payload operatorCursorPayload
	if err := json.Unmarshal(encoded, &payload); err != nil || payload.Version != 1 {
		return nil, operatorError(connect.CodeInvalidArgument, "invalid_argument", "invalid page_token", "page_token")
	}
	if payload.CustomerID != customerID || payload.ClusterID != clusterID || payload.LifecycleStatus != filterLifecycleString(filter) || payload.SessionStatus != filterSessionString(filter) {
		return nil, operatorError(connect.CodeInvalidArgument, "invalid_argument", "page_token scope does not match request", "page_token")
	}
	registeredAt, err := time.Parse(time.RFC3339Nano, payload.RegisteredAt)
	if err != nil || payload.OperatorID == "" {
		return nil, operatorError(connect.CodeInvalidArgument, "invalid_argument", "invalid page_token", "page_token")
	}
	return &store.OperatorCursor{
		CustomerID:      customerID,
		ClusterID:       clusterID,
		NoSession:       filter.NoSession,
		LifecycleStatus: filter.LifecycleStatus,
		SessionStatus:   filter.SessionStatus,
		RegisteredAt:    registeredAt,
		OperatorID:      payload.OperatorID,
	}, nil
}

func encodeOperatorCursor(cursor *store.OperatorCursor) string {
	if cursor == nil {
		return ""
	}
	payload, err := json.Marshal(operatorCursorPayload{
		Version:         1,
		CustomerID:      cursor.CustomerID,
		ClusterID:       cursor.ClusterID,
		LifecycleStatus: filterLifecycleString(store.OperatorListFilter{LifecycleStatus: cursor.LifecycleStatus}),
		SessionStatus:   filterSessionString(store.OperatorListFilter{SessionStatus: cursor.SessionStatus, NoSession: cursor.NoSession}),
		RegisteredAt:    cursor.RegisteredAt.UTC().Format(time.RFC3339Nano),
		OperatorID:      cursor.OperatorID,
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func operatorError(code connect.Code, errorCode, message, field string) error {
	connectErr := connect.NewError(code, errors.New(message))
	detail := &orchestratorv1.OperatorErrorDetail{ErrorCode: errorCode}
	if field != "" {
		detail.FieldViolations = []*commonv1.FieldViolation{{Field: field, Description: message}}
	}
	if errorDetail, err := connect.NewErrorDetail(detail); err == nil {
		connectErr.AddDetail(errorDetail)
	}
	return connectErr
}

func mapOperatorStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrPendingTokenExists):
		return operatorError(connect.CodeAlreadyExists, "pending_token_exists", "a pending enrollment token already exists", "")
	case errors.Is(err, store.ErrDuplicateOperatorName):
		return operatorError(connect.CodeAlreadyExists, "duplicate_operator_name", "an active operator with this name already exists", "operator_name")
	case errors.Is(err, store.ErrTokenReplaceConflict):
		return operatorError(connect.CodeAborted, "token_replace_conflict", "the pending enrollment token changed", "")
	case errors.Is(err, store.ErrOperatorNotFound):
		return operatorError(connect.CodeNotFound, "operator_not_found", "operator not found", "")
	case errors.Is(err, store.ErrOperatorStateConflict):
		return operatorError(connect.CodeFailedPrecondition, "operator_state_conflict", "operator state changed", "")
	case errors.Is(err, store.ErrAuditUnavailable):
		return operatorError(connect.CodeUnavailable, "audit_unavailable", "audit service is unavailable", "")
	default:
		return operatorError(connect.CodeInternal, "internal", "operator management failed", "")
	}
}

func operatorLifecycleFromProto(value orchestratorv1.OperatorLifecycleStatus) (store.OperatorStatus, bool) {
	switch value {
	case orchestratorv1.OperatorLifecycleStatus_OPERATOR_LIFECYCLE_STATUS_ACTIVE:
		return store.OperatorActive, true
	case orchestratorv1.OperatorLifecycleStatus_OPERATOR_LIFECYCLE_STATUS_SUPERSEDED:
		return store.OperatorSuperseded, true
	case orchestratorv1.OperatorLifecycleStatus_OPERATOR_LIFECYCLE_STATUS_REVOKED:
		return store.OperatorRevoked, true
	default:
		return "", false
	}
}

func operatorSessionStatusFromProto(value orchestratorv1.OperatorSessionStatus) (store.SessionStatus, bool) {
	switch value {
	case orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_ONLINE:
		return store.SessionOnline, true
	case orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_SUSPECT:
		return store.SessionSuspect, true
	case orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_OFFLINE:
		return store.SessionOffline, true
	case orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_REVOKED:
		return store.SessionRevoked, true
	default:
		return "", false
	}
}

func operatorLifecycleToProto(value store.OperatorStatus) orchestratorv1.OperatorLifecycleStatus {
	switch value {
	case store.OperatorActive:
		return orchestratorv1.OperatorLifecycleStatus_OPERATOR_LIFECYCLE_STATUS_ACTIVE
	case store.OperatorSuperseded:
		return orchestratorv1.OperatorLifecycleStatus_OPERATOR_LIFECYCLE_STATUS_SUPERSEDED
	case store.OperatorRevoked:
		return orchestratorv1.OperatorLifecycleStatus_OPERATOR_LIFECYCLE_STATUS_REVOKED
	default:
		return orchestratorv1.OperatorLifecycleStatus_OPERATOR_LIFECYCLE_STATUS_UNSPECIFIED
	}
}

func operatorSessionStatusToProto(value store.SessionStatus) orchestratorv1.OperatorSessionStatus {
	switch value {
	case store.SessionOnline:
		return orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_ONLINE
	case store.SessionSuspect:
		return orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_SUSPECT
	case store.SessionOffline:
		return orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_OFFLINE
	case store.SessionRevoked:
		return orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_REVOKED
	default:
		return orchestratorv1.OperatorSessionStatus_OPERATOR_SESSION_STATUS_UNSPECIFIED
	}
}

func operatorSessionReasonToProto(value *store.SessionStatusReason) orchestratorv1.OperatorSessionStatusReason {
	if value == nil {
		return orchestratorv1.OperatorSessionStatusReason_OPERATOR_SESSION_STATUS_REASON_UNSPECIFIED
	}
	switch *value {
	case store.SessionReasonNoSession:
		return orchestratorv1.OperatorSessionStatusReason_OPERATOR_SESSION_STATUS_REASON_NO_SESSION
	case store.SessionReasonHeartbeatTimeout:
		return orchestratorv1.OperatorSessionStatusReason_OPERATOR_SESSION_STATUS_REASON_HEARTBEAT_TIMEOUT
	case store.SessionReasonHeartbeatDelayed:
		return orchestratorv1.OperatorSessionStatusReason_OPERATOR_SESSION_STATUS_REASON_HEARTBEAT_DELAYED
	case store.SessionReasonCertRevoked:
		return orchestratorv1.OperatorSessionStatusReason_OPERATOR_SESSION_STATUS_REASON_CERTIFICATE_REVOKED
	case store.SessionReasonOperatorSuperseded:
		return orchestratorv1.OperatorSessionStatusReason_OPERATOR_SESSION_STATUS_REASON_OPERATOR_SUPERSEDED
	case store.SessionReasonSessionReplaced:
		return orchestratorv1.OperatorSessionStatusReason_OPERATOR_SESSION_STATUS_REASON_SESSION_REPLACED
	default:
		return orchestratorv1.OperatorSessionStatusReason_OPERATOR_SESSION_STATUS_REASON_UNKNOWN
	}
}

func enrollmentTokenStateToProto(value store.EnrollmentTokenState) orchestratorv1.EnrollmentTokenState {
	switch value {
	case store.TokenStatePending:
		return orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_PENDING
	case store.TokenStateUsed:
		return orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_USED
	case store.TokenStateExpired:
		return orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_EXPIRED
	case store.TokenStateRevoked:
		return orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_REVOKED
	default:
		return orchestratorv1.EnrollmentTokenState_ENROLLMENT_TOKEN_STATE_UNSPECIFIED
	}
}

func filterLifecycleString(filter store.OperatorListFilter) string {
	if filter.LifecycleStatus == nil {
		return ""
	}
	return string(*filter.LifecycleStatus)
}

func filterSessionString(filter store.OperatorListFilter) string {
	if filter.NoSession {
		return "none"
	}
	if filter.SessionStatus == nil {
		return ""
	}
	return string(*filter.SessionStatus)
}

func generateEnrollmentToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate enrollment token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
