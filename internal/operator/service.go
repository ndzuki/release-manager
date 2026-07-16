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
//
// On reconnect, it detects sequence gaps, re-delivers unacknowledged commands,
// and returns duplicate results for already-completed commands.
//nolint:gocyclo // bidirectional stream state machine inherently complex
func (s *Service) CommandStream(
	ctx context.Context,
	stream *connect.BidiStream[operatorv1.CommandStreamRequest, operatorv1.CommandStreamResponse],
) error {
	// ── Hello phase ──
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
	lastSeenSeq := hello.GetLastSeenSequence()

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

	s.logger.Info("operator stream established",
		"operator_id", operatorID, "session_id", sessionID,
		"last_seen_sequence", lastSeenSeq,
	)

	// ── Reconnect: detect sequence gap and re-deliver ──
	if err := s.handleReconnect(ctx, stream, operatorID, lastSeenSeq); err != nil {
		return err
	}

	// ── Main loop ──
	// Heartbeat ticker.
	hbTicker := time.NewTicker(s.heartbeatMaxAge / 2)
	defer hbTicker.Stop()

	// Deliver pending commands in a background goroutine.
	deliverCh := make(chan *store.OutboxEntry, 16)
	deliverDone := make(chan struct{})
	defer close(deliverDone)

	go s.deliverPending(ctx, operatorID, deliverCh, deliverDone)

	for {
		select {
		case <-hbTicker.C:
			if err := s.store.Sessions().Heartbeat(ctx, sessionID); err != nil {
				s.logger.Warn("heartbeat failed", "error", err)
				_ = s.store.Sessions().UpdateStatus(ctx, sessionID, store.SessionOffline)
				return nil
			}

		case entry := <-deliverCh:
			s.logger.Debug("delivering command",
				"outbox_id", entry.ID,
				"command_id", entry.CommandID,
				"sequence", entry.Sequence,
			)
			if err := s.sendCommand(stream, entry); err != nil {
				return err
			}
			_ = s.store.Outbox().UpdateStatus(ctx, entry.ID, store.CommandDelivered, "")

		case <-ctx.Done():
			return nil

		default:
			req, err := stream.Receive()
			if err != nil {
				return err
			}

			switch {
			case req.GetHeartbeat() != nil:
				_ = s.store.Sessions().Heartbeat(ctx, sessionID)

			case req.GetAck() != nil:
				ack := req.GetAck()
				s.logger.Debug("command ack",
					"outbox_id", ack.GetOutboxId(),
					"sequence", ack.GetSequence(),
					"ack_type", ack.GetAckType(),
				)
				// ACK_RECEIVED and ACK_PERSISTED both imply delivered,
				// but only ACK_PERSISTED releases the orchestator from
				// re-delivery responsibility.
				if ack.GetAckType() == operatorv1.AckType_ACK_TYPE_PERSISTED {
					_ = s.store.Outbox().UpdateStatus(ctx, ack.GetOutboxId(), store.CommandPersisted, "")
				}

			case req.GetResult() != nil:
				result := req.GetResult()
				s.logger.Info("command result",
					"outbox_id", result.GetOutboxId(),
					"command_id", result.GetCommandId(),
					"status", result.GetStatus(),
					"sequence", result.GetSequence(),
				)

				// Dedup: check if this command already reached terminal state.
				existing, err := s.store.Outbox().GetByCommandID(ctx, result.GetCommandId())
				if err != nil && err != store.ErrNotFound {
					s.logger.Warn("failed to check command dedup", "error", err)
				}
				if existing != nil && (existing.Status == store.CommandSucceeded || existing.Status == store.CommandFailed) {
					s.logger.Info("command already terminal, sending duplicate response",
						"command_id", result.GetCommandId(),
					)
					if err := stream.Send(&operatorv1.CommandStreamResponse{
						Payload: &operatorv1.CommandStreamResponse_DuplicateResponse{
							DuplicateResponse: &operatorv1.DuplicateResponse{
								CommandId:  result.GetCommandId(),
								ResultJson: existing.ResultJSON,
							},
						},
					}); err != nil {
						return err
					}
					continue
				}

				status := store.CommandSucceeded
				if result.GetStatus() == "failed" {
					status = store.CommandFailed
				}
				resultJSON := result.GetResultJson()
				if resultJSON == "" {
					resultJSON = result.GetMessage()
				}
				_ = s.store.Outbox().UpdateStatus(ctx, result.GetOutboxId(), status, resultJSON)

			case req.GetResyncResponse() != nil:
				rr := req.GetResyncResponse()
				s.logger.Info("operator resync response",
					"operator_last_sequence", rr.GetOperatorLastSequence(),
				)
				// After receiving resync response, re-deliver any unacked
				// commands starting from the operator's last sequence.
				_ = s.reDeliverFrom(ctx, stream, operatorID, rr.GetOperatorLastSequence())
			}
		}
	}
}

// handleReconnect checks for sequence gaps and re-delivers unacknowledged commands
// on session reconnect. It sends ResyncRequest for gaps and DuplicateResponse for
// already-completed commands.
func (s *Service) handleReconnect(
	ctx context.Context,
	stream *connect.BidiStream[operatorv1.CommandStreamRequest, operatorv1.CommandStreamResponse],
	operatorID string,
	lastSeenSeq int64,
) error {
	// Get the max sequence known to orchestrator.
	maxSeq, err := s.store.Outbox().GetNextSequence(ctx)
	if err != nil {
		s.logger.Warn("failed to get next sequence", "error", err)
		return nil // non-fatal on reconnect
	}
	// GetNextSequence returns max+1, so current max is maxSeq-1.
	if maxSeq > 0 {
		maxSeq-- // actual max known sequence
	}

	// Detect sequence gap: operator missed some commands.
	if lastSeenSeq > 0 && lastSeenSeq < maxSeq {
		s.logger.Warn("sequence gap detected",
			"operator_last", lastSeenSeq,
			"orchestrator_max", maxSeq,
		)
		if err := stream.Send(&operatorv1.CommandStreamResponse{
			Payload: &operatorv1.CommandStreamResponse_ResyncRequest{
				ResyncRequest: &operatorv1.ResyncRequest{
					OrchestratorLastSequence: maxSeq,
					Reason:                   fmt.Sprintf("gap: operator has %d, orchestrator has %d", lastSeenSeq, maxSeq),
				},
			},
		}); err != nil {
			return err
		}
		// Don't re-deliver yet — wait for operator's ResyncResponse.
		return nil
	}

	// No gap: re-deliver delivered but not yet ACK_PERSISTED commands.
	return s.reDeliverFrom(ctx, stream, operatorID, lastSeenSeq)
}

// reDeliverFrom sends all delivered-but-not-acked commands to the operator,
// skipping commands already completed (sending DuplicateResponse instead).
func (s *Service) reDeliverFrom(
	ctx context.Context,
	stream *connect.BidiStream[operatorv1.CommandStreamRequest, operatorv1.CommandStreamResponse],
	operatorID string,
	fromSeq int64,
) error {
	entries, err := s.store.Outbox().GetDeliveredNotAcked(ctx, operatorID)
	if err != nil {
		return fmt.Errorf("query delivered not acked: %w", err)
	}

	for _, entry := range entries {
		if entry.Sequence <= fromSeq {
			continue
		}

		// If already terminal, send duplicate response instead.
		if entry.Status == store.CommandSucceeded || entry.Status == store.CommandFailed {
			if err := stream.Send(&operatorv1.CommandStreamResponse{
				Payload: &operatorv1.CommandStreamResponse_DuplicateResponse{
					DuplicateResponse: &operatorv1.DuplicateResponse{
						CommandId:  entry.CommandID,
						ResultJson: entry.ResultJSON,
					},
				},
			}); err != nil {
				return err
			}
			continue
		}

		// Re-deliver.
		if err := s.sendCommand(stream, entry); err != nil {
			return err
		}
		_ = s.store.Outbox().UpdateStatus(ctx, entry.ID, store.CommandDelivered, "")
	}
	return nil
}

// sendCommand serializes an outbox entry into a CommandStreamResponse.
func (s *Service) sendCommand(
	stream *connect.BidiStream[operatorv1.CommandStreamRequest, operatorv1.CommandStreamResponse],
	entry *store.OutboxEntry,
) error {
	return stream.Send(&operatorv1.CommandStreamResponse{
		Payload: &operatorv1.CommandStreamResponse_Command{
			Command: &operatorv1.Command{
				OutboxId:      entry.ID,
				CommandId:     entry.CommandID,
				OperationId:   entry.OperationID,
				OperationType: entry.OperationType,
				Sequence:      entry.Sequence,
			},
		},
	})
}

// deliverPending polls for pending commands and sends them to the delivery channel.
// It enforces max_inflight by blocking when an inflight command exists.
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
			// Check for inflight command (max_inflight=1 enforcement).
			inflight, err := s.store.Outbox().GetInflightForOperator(ctx, operatorID)
			if err != nil && err != store.ErrNotFound {
				s.logger.Warn("failed to check inflight", "error", err)
				continue
			}
			if inflight != nil {
				// An inflight command exists; skip this poll cycle.
				continue
			}

			entry, err := s.store.Outbox().GetNextPending(ctx, operatorID)
			if err != nil || entry == nil {
				continue
			}

			// Assign sequence number to new pending entries.
			if entry.Sequence == 0 {
				seq, err := s.store.Outbox().GetNextSequence(ctx)
				if err != nil {
					s.logger.Warn("failed to get next sequence", "error", err)
					continue
				}
				entry.Sequence = seq
				// Update the sequence in the outbox.
				_ = s.store.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPending, "")
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
