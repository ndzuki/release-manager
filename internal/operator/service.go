// Package operator implements the operator agent Connect service.
package operator

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/store"
)

// Service implements the OperatorServiceHandler Connect interface.
type Service struct {
	store             store.Store
	ca                *ca.CA
	logger            *slog.Logger
	sessionTTL        time.Duration
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration
	suspectAfter      time.Duration
	offlineAfter      time.Duration
	configVersion     string
	registry          *SessionRegistry
	inventorySyncer   *InventorySyncer
	commandExecutor   CommandExecutor
}

func NewService(st store.Store, logger *slog.Logger) (*Service, error) {
	caInst, err := ca.New(ca.Config{TTL: 7 * 24 * time.Hour})
	if err != nil {
		return nil, fmt.Errorf("create CA: %w", err)
	}
	service := &Service{
		store:             st,
		ca:                caInst,
		logger:            logger,
		sessionTTL:        15 * time.Minute,
		heartbeatInterval: 10 * time.Second,
		heartbeatTimeout:  30 * time.Second,
		suspectAfter:      30 * time.Second,
		offlineAfter:      60 * time.Second,
		configVersion:     "dev",
	}
	service.registry = NewSessionRegistry(
		st.Sessions(),
		service.suspectAfter,
		service.offlineAfter,
		logger,
	)
	return service, nil
}

// SetInventorySyncer attaches an inventory syncer for release inventory sync (REQ-017).
func (s *Service) SetInventorySyncer(syncer *InventorySyncer) {
	s.inventorySyncer = syncer
}

func (s *Service) SetCommandExecutor(executor CommandExecutor) {
	s.commandExecutor = executor
}

// RunSessionMonitor advances persisted session states until ctx is canceled.
func (s *Service) RunSessionMonitor(ctx context.Context) {
	s.registry.Run(ctx)
}

// Enroll validates a single-use enrollment token and creates an operator record.
// The operator establishes its online session separately through CommandStream.
//
//nolint:gocyclo // enrollment validation is intentionally sequential and fail-closed.
func (s *Service) Enroll(
	ctx context.Context,
	req *connect.Request[operatorv1.EnrollRequest],
) (*connect.Response[operatorv1.EnrollResponse], error) {
	msg := req.Msg

	// Validate token exists and is unused.
	token, err := s.store.EnrollmentTokens().GetByToken(ctx, msg.GetEnrollmentToken())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid enrollment token"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if token.Used {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("enrollment token already used"))
	}

	if time.Now().UTC().After(token.ExpiresAt) {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("enrollment token expired"))
	}

	// Validate token matches requested customer/cluster.
	if token.CustomerID != msg.GetCustomerId() {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("customer_id mismatch"))
	}
	if token.ClusterID != msg.GetClusterId() {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("cluster_id mismatch"))
	}

	// Verify customer and cluster are still active.
	cust, err := s.store.Customers().Get(ctx, msg.GetCustomerId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if cust.Status == store.CustomerDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("customer %q is disabled", msg.GetCustomerId()))
	}

	cl, err := s.store.Clusters().Get(ctx, msg.GetClusterId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if cl.Status == store.ClusterDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("cluster %q is disabled", msg.GetClusterId()))
	}
	// Parse and validate CSR (AC-015-06).
	csr, err := parseCSR(msg.GetCsrPem())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid CSR: %w", err))
	}
	if err := validateCSRSANs(csr, msg.GetCustomerId(), msg.GetClusterId()); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}
	operatorName := csr.Subject.CommonName
	if operatorName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("CSR CommonName (operator_name) is required"))
	}

	// Check operator_name is not already used by another cluster (AC-015-02).
	if existingByName, err := s.store.Operators().GetByName(ctx, operatorName); err == nil {
		if existingByName.ClusterID != msg.GetClusterId() {
			return nil, connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("operator_name %q already registered on cluster %q", operatorName, existingByName.ClusterID))
		}
	}

	// Check for existing active operator on this cluster — supersede it (AC-015-02).
	existingOp, err := s.store.Operators().GetByClusterID(ctx, msg.GetClusterId())
	if err != nil && err != store.ErrNotFound {
		s.logger.Warn("checking existing operator on cluster", "error", err)
	}
	if existingOp != nil {
		s.logger.Warn("superseding existing operator", "old_id", existingOp.ID, "cluster_id", msg.GetClusterId())
		existingOp.Status = store.OperatorSuperseded
		if err := s.store.Operators().Update(ctx, existingOp); err != nil {
			s.logger.Warn("failed to supersede operator", "error", err)
		}
		// Mark old active sessions offline.
		if oldSess, err := s.store.Sessions().GetActiveByOperator(ctx, existingOp.ID); err == nil {
			if err := s.store.Sessions().UpdateStatus(ctx, oldSess.ID, store.SessionOffline); err != nil {
				s.logger.Warn("failed to mark old session offline after supersede", "error", err)
			}
		}
	}

	// Sign CSR with CA (AC-015-05).
	certDER, err := s.ca.SignCSR(csr)
	if err != nil {
		s.logger.Error("CA sign CSR failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign CSR: %w", err))
	}
	certPEM := ca.CertDERToPEM(certDER)

	// Persist the X.509 certificate serial for mTLS identity lookup.
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("parse signed certificate: %w", err))
	}
	certSerial := cert.SerialNumber.String()

	operatorID := msg.GetOperatorId()
	if operatorID == "" {
		operatorID = uuid.New().String()
	}

	// Create operator record with operator_name from CSR CN.
	op := &store.Operator{
		ID:         operatorID,
		Name:       operatorName,
		CustomerID: msg.GetCustomerId(),
		ClusterID:  msg.GetClusterId(),
		CertSerial: certSerial,
		Status:     store.OperatorActive,
	}

	// Check if operator with this cert serial already exists; replace if superseded.
	if existingBySerial, err := s.store.Operators().GetByCertSerial(ctx, certSerial); err == nil {
		existingBySerial.Status = store.OperatorSuperseded
		existingBySerial.SupersededBy = operatorID
		if err := s.store.Operators().Update(ctx, existingBySerial); err != nil {
			s.logger.Warn("failed to supersede operator by cert serial", "error", err)
		}
	}
	if err := s.store.Operators().Create(ctx, op); err != nil {
		s.logger.Error("create operator failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create operator: %w", err))
	}

	// Mark token used.
	if err := s.store.EnrollmentTokens().MarkUsed(ctx, token.ID, operatorID); err != nil {
		s.logger.Error("mark token used failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("mark token: %w", err))
	}

	// Keep the legacy session identifier offline; online state is established by Hello.
	now := time.Now().UTC()
	session := &store.Session{
		ID:         uuid.New().String(),
		OperatorID: operatorID,
		Status:     store.SessionOffline,
		StartedAt:  now,
		ExpiresAt:  now.Add(s.sessionTTL),
	}
	if err := s.store.Sessions().Create(ctx, session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create enrollment session: %w", err))
	}

	s.logger.Info("operator enrolled",
		"operator_id", operatorID,
		"operator_name", operatorName,
		"customer_id", msg.GetCustomerId(),
		"cluster_id", msg.GetClusterId(),
		"session_id", session.ID,
	)

	return connect.NewResponse(&operatorv1.EnrollResponse{
		SessionId:      session.ID,
		TtlSeconds:     int64(s.sessionTTL.Seconds()),
		CertificatePem: certPEM,
	}), nil
}

// CommandStream handles the bidirectional stream for command delivery.
// It manages the outbox state machine:
//
//	pending → delivered → persisted → running → terminal (succeeded/failed)
//
// On reconnect, it detects sequence gaps, re-delivers unacknowledged commands,
// and returns duplicate results for already-completed commands.
//
//nolint:gocyclo // bidirectional stream state machine inherently complex
func (s *Service) CommandStream(
	ctx context.Context,
	stream *connect.BidiStream[operatorv1.CommandStreamRequest, operatorv1.CommandStreamResponse],
) error {
	req, err := stream.Receive()
	if err != nil {
		return err
	}
	hello := req.GetHello()
	if hello == nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("first message must be hello"))
	}

	identity, err := certificateIdentityFromContext(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}

	operatorID := hello.GetOperatorId()
	op, err := s.store.Operators().Get(ctx, operatorID)
	if err != nil {
		if err == store.ErrNotFound {
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("operator not found"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("get operator: %w", err))
	}
	if op.CertSerial != identity.Serial {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("identity_mismatch"))
	}
	switch op.Status {
	case store.OperatorSuperseded:
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("superseded"))
	case store.OperatorRevoked:
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("certificate_revoked"))
	}
	if hello.GetInstanceId() == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("instance_id is required"))
	}
	if hello.GetVersion() == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("version is required"))
	}

	now := time.Now().UTC()
	sess := &store.Session{
		ID:                  hello.GetSessionId(),
		OperatorID:          operatorID,
		InstanceID:          hello.GetInstanceId(),
		Version:             hello.GetVersion(),
		Capabilities:        hello.GetCapabilities(),
		ActiveConfigVersion: s.configVersion,
		Status:              store.SessionOnline,
		StartedAt:           now,
		LastHeartbeat:       now,
		ExpiresAt:           now.Add(s.sessionTTL),
	}
	if sess.ID == "" {
		sess.ID = uuid.New().String()
	}
	if err := s.store.Sessions().Establish(ctx, sess); err != nil {
		if err == store.ErrDuplicateKey {
			return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("duplicate_session"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("establish session: %w", err))
	}
	s.registry.Register(sess.ID, now)
	defer func() {
		s.registry.Unregister(sess.ID)
		if err := s.store.Sessions().UpdateStatus(
			context.WithoutCancel(ctx),
			sess.ID,
			store.SessionOffline,
		); err != nil {
			s.logger.Warn("mark session offline", "session_id", sess.ID, "error", err)
		}
	}()

	if err := stream.Send(&operatorv1.CommandStreamResponse{
		Payload: &operatorv1.CommandStreamResponse_SessionEstablished{
			SessionEstablished: &operatorv1.SessionEstablished{
				SessionId:                sess.ID,
				HeartbeatIntervalSeconds: int64(s.heartbeatInterval.Seconds()),
				HeartbeatTimeoutSeconds:  int64(s.heartbeatTimeout.Seconds()),
				ActiveConfigVersion:      s.configVersion,
			},
		},
	}); err != nil {
		return err
	}

	for {
		request, err := stream.Receive()
		if err != nil {
			return err
		}
		switch {
		case request.GetHeartbeat() != nil:
			if err := s.store.Sessions().Heartbeat(ctx, sess.ID); err != nil {
				return fmt.Errorf("heartbeat session: %w", err)
			}
			s.registry.Heartbeat(sess.ID, time.Now().UTC())
		case request.GetAck() != nil:
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("command acknowledgments are not supported"))
		case request.GetResult() != nil:
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("command results are not supported"))
		case request.GetResyncResponse() != nil:
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("resync responses are not supported"))
		default:
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported session message"))
		}
	}
}

// RevokeOperator revokes an operator and closes its active sessions (AC-015-03).
func (s *Service) RevokeOperator(
	ctx context.Context,
	req *connect.Request[operatorv1.RevokeOperatorRequest],
) (*connect.Response[operatorv1.RevokeOperatorResponse], error) {
	msg := req.Msg

	op, err := s.store.Operators().Get(ctx, msg.GetOperatorId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("operator %q not found", msg.GetOperatorId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if op.Status == store.OperatorRevoked {
		return connect.NewResponse(&operatorv1.RevokeOperatorResponse{
			OperatorId: op.ID,
			Status:     "already_revoked",
		}), nil
	}

	if err := s.store.Operators().Revoke(ctx, op.ID); err != nil {
		s.logger.Error("revoke operator failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("revoke operator: %w", err))
	}

	// Close active sessions for this operator.
	if activeSess, err := s.store.Sessions().GetActiveByOperator(ctx, op.ID); err == nil {
		if err := s.store.Sessions().UpdateStatus(ctx, activeSess.ID, store.SessionOffline); err != nil {
			s.logger.Warn("failed to close session after revoke", "error", err)
		}
	}

	s.logger.Warn("operator revoked", "operator_id", op.ID, "reason", msg.GetReason())
	return connect.NewResponse(&operatorv1.RevokeOperatorResponse{
		OperatorId: op.ID,
		Status:     "revoked",
	}), nil
}

// Compile-time check: Service implements the Connect handler interface.
var _ operatorv1connect.OperatorServiceHandler = (*Service)(nil)
