// Package operator implements the operator agent Connect service.
package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/store"
)

// Service implements the OperatorServiceHandler Connect interface.
type Service struct {
	store           store.Store
	logger          *slog.Logger
	sessionTTL      time.Duration
	heartbeatMaxAge time.Duration
	suspectAfter    time.Duration
	inventorySyncer *InventorySyncer
}

// NewService creates a new operator Connect service.
func NewService(st store.Store, logger *slog.Logger) *Service {
	return &Service{
		store:           st,
		logger:          logger,
		sessionTTL:      15 * time.Minute,
		heartbeatMaxAge: 30 * time.Second,
		suspectAfter:    60 * time.Second,
	}
}

// SetInventorySyncer attaches an inventory syncer for release inventory sync (REQ-017).
func (s *Service) SetInventorySyncer(syncer *InventorySyncer) {
	s.inventorySyncer = syncer
}

// Enroll validates a single-use enrollment token, creates an operator record,
// and establishes a new session for the operator agent.
//nolint:gocyclo // enrollment validation requires multiple sequential checks
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

	// Check for existing active operator on this cluster — supersede it.
	existingSess, err := s.store.Sessions().GetActiveByOperator(ctx, msg.GetOperatorId())
	if err != nil && err != store.ErrNotFound {
		s.logger.Warn("checking existing session", "error", err)
	}
	if existingSess != nil {
		s.logger.Warn("superseding existing session", "operator_id", msg.GetOperatorId(), "session_id", existingSess.ID)
		if err := s.store.Sessions().UpdateStatus(ctx, existingSess.ID, store.SessionOffline); err != nil {
			s.logger.Warn("failed to mark old session offline", "error", err)
		}
	}

	// Generate a cert serial from CSR hash.
	certSerial := hashBytes(msg.GetCsrPem())
	if len(certSerial) > 10 {
		certSerial = certSerial[:10]
	}

	operatorID := msg.GetOperatorId()
	if operatorID == "" {
		operatorID = uuid.New().String()
	}

	// Create operator record.
	op := &store.Operator{
		ID:         operatorID,
		CustomerID: msg.GetCustomerId(),
		ClusterID:  msg.GetClusterId(),
		CertSerial: certSerial,
		Status:     store.OperatorActive,
	}

	// Check if operator with this cert serial already exists; replace if superseded.
	if existingOp, err := s.store.Operators().GetByCertSerial(ctx, certSerial); err == nil {
		existingOp.Status = store.OperatorSuperseded
		existingOp.SupersededBy = operatorID
		if err := s.store.Operators().Update(ctx, existingOp); err != nil {
			s.logger.Warn("failed to supersede operator", "error", err)
		}
		// Mark old sessions as offline.
		if existingSess != nil && existingSess.OperatorID != operatorID {
			if err := s.store.Sessions().UpdateStatus(ctx, existingSess.ID, store.SessionOffline); err != nil {
				s.logger.Warn("failed to mark old session offline after supersede", "error", err)
			}
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

	// Create session.
	now := time.Now().UTC()
	session := &store.Session{
		ID:            uuid.New().String(),
		OperatorID:    operatorID,
		Status:        store.SessionOnline,
		StartedAt:     now,
		LastHeartbeat: now,
		ExpiresAt:     now.Add(s.sessionTTL),
	}
	if err := s.store.Sessions().Create(ctx, session); err != nil {
		s.logger.Error("create session failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
	}

	s.logger.Info("operator enrolled",
		"operator_id", operatorID,
		"customer_id", msg.GetCustomerId(),
		"cluster_id", msg.GetClusterId(),
		"session_id", session.ID,
	)

	return connect.NewResponse(&operatorv1.EnrollResponse{
		SessionId:      session.ID,
		TtlSeconds:     int64(s.sessionTTL.Seconds()),
		CertificatePem: []byte{}, // actual cert signing deferred to CA integration
	}), nil
}

// CommandStream handles the bidirectional stream for command delivery.
// It manages the outbox state machine:
//
//	pending → delivered → persisted → running → terminal (succeeded/failed)
//nolint:gocyclo // bidirectional stream state machine inherently complex
func (s *Service) CommandStream(
	ctx context.Context,
	stream *connect.BidiStream[operatorv1.CommandStreamRequest, operatorv1.CommandStreamResponse],
) error {
	// Initial hello phase.
	req, err := stream.Receive()
	if err != nil {
		return err
	}

	hello := req.GetHello()
	if hello == nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("first message must be Hello"))
	}

	sessionID := hello.GetSessionId()
	operatorID := hello.GetOperatorId()

	// Validate session.
	sess, err := s.store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid session: %w", err))
	}
	if sess.OperatorID != operatorID {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("session operator mismatch"))
	}
	if sess.Status != store.SessionOnline {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("session is %s, not online", sess.Status))
	}

	s.logger.Info("operator stream established", "operator_id", operatorID, "session_id", sessionID)

	// Heartbeat ticker.
	hbTicker := time.NewTicker(s.heartbeatMaxAge / 2)
	defer hbTicker.Stop()

	// Deliver pending commands in a background goroutine.
	deliverCh := make(chan *store.OutboxEntry, 16)
	deliverDone := make(chan struct{})
	defer close(deliverDone)

	go s.deliverPending(ctx, operatorID, deliverCh, deliverDone)

	// Main receive loop: process heartbeats, ACKs, and results.
	for {
		// Check for pending commands to deliver.
		select {
		case entry := <-deliverCh:
			s.logger.Debug("delivering command", "outbox_id", entry.ID, "operation_id", entry.OperationID)
			if err := stream.Send(&operatorv1.CommandStreamResponse{
				Payload: &operatorv1.CommandStreamResponse_Command{
					Command: &operatorv1.Command{
						OutboxId:      entry.ID,
						CommandId:     entry.OperationID,
						OperationId:   entry.OperationID,
						OperationType: "UPGRADE",
					},
				},
			}); err != nil {
				return err
			}
			if err := s.store.Outbox().UpdateStatus(ctx, entry.ID, store.CommandDelivered, ""); err != nil {
				s.logger.Warn("failed to mark command delivered", "error", err)
			}
		default:
		}

		select {
		case <-hbTicker.C:
			if err := s.store.Sessions().Heartbeat(ctx, sessionID); err != nil {
				s.logger.Warn("heartbeat failed", "error", err)
				if err := s.store.Sessions().UpdateStatus(ctx, sessionID, store.SessionOffline); err != nil {
					s.logger.Warn("failed to mark session offline on heartbeat timeout", "error", err)
				}
				return nil
			}

		case entry := <-deliverCh:
			s.logger.Debug("delivering command", "outbox_id", entry.ID)
			if err := stream.Send(&operatorv1.CommandStreamResponse{
				Payload: &operatorv1.CommandStreamResponse_Command{
					Command: &operatorv1.Command{
						OutboxId:      entry.ID,
						CommandId:     entry.OperationID,
						OperationId:   entry.OperationID,
						OperationType: "UPGRADE",
					},
				},
			}); err != nil {
				return err
			}
			if err := s.store.Outbox().UpdateStatus(ctx, entry.ID, store.CommandDelivered, ""); err != nil {
				s.logger.Warn("failed to mark command delivered", "error", err)
			}

		case <-ctx.Done():
			return nil

		default:
			// Non-blocking receive.
			req, err := stream.Receive()
			if err != nil {
				return err
			}

			switch {
			case req.GetHeartbeat() != nil:
				if err := s.store.Sessions().Heartbeat(ctx, sessionID); err != nil {
					s.logger.Warn("heartbeat failed", "error", err)
				}

			case req.GetAck() != nil:
				ack := req.GetAck()
				s.logger.Debug("command ack", "outbox_id", ack.GetOutboxId())
				if err := s.store.Outbox().UpdateStatus(ctx, ack.GetOutboxId(), store.CommandPersisted, ""); err != nil {
					s.logger.Warn("failed to mark command persisted", "error", err)
				}

			case req.GetResult() != nil:
				result := req.GetResult()
				status := store.CommandSucceeded
				if result.GetStatus() == "failed" {
					status = store.CommandFailed
				}
				s.logger.Info("command result",
					"outbox_id", result.GetOutboxId(),
					"status", result.GetStatus(),
				)
				if err := s.store.Outbox().UpdateStatus(ctx, result.GetOutboxId(), status, result.GetMessage()); err != nil {
					s.logger.Warn("failed to mark command result", "error", err)
				}
			}
		}
	}
}

// deliverPending polls for pending commands and sends them to the stream.
func (s *Service) deliverPending(
	ctx context.Context,
	operatorID string,
	deliverCh chan<- *store.OutboxEntry,
	done <-chan struct{},
) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			entry, err := s.store.Outbox().GetNextPending(ctx, operatorID)
			if err != nil || entry == nil {
				continue
			}
			select {
			case deliverCh <- entry:
			case <-done:
				return
			}
		}
	}
}

// hashBytes returns a hex-encoded SHA-256 of the input.
func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Compile-time check: Service implements the Connect handler interface.
var _ operatorv1connect.OperatorServiceHandler = (*Service)(nil)
