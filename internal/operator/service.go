// Package operator implements the operator agent Connect service.
package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/orchestrator/operation"
	"github.com/ndzuki/release-manager/internal/store"
	"google.golang.org/protobuf/encoding/protojson"
)

// Service implements the OperatorServiceHandler Connect interface.
type Service struct {
	store            store.Store
	ca               *ca.CA
	logger           *slog.Logger
	sessionTTL       time.Duration
	heartbeatMaxAge  time.Duration
	suspectAfter     time.Duration
	inventorySyncer  *InventorySyncer
	commandExecutor  CommandExecutor
	streamMu         sync.RWMutex
	emergencyStreams map[string]chan *operatorv1.EmergencyCommand
}

// NewService creates a new operator Connect service with a self-signed CA.
func NewService(st store.Store, logger *slog.Logger) (*Service, error) {
	caInst, err := ca.New(ca.Config{TTL: 7 * 24 * time.Hour})
	if err != nil {
		return nil, fmt.Errorf("create CA: %w", err)
	}
	return &Service{
		store:            st,
		ca:               caInst,
		logger:           logger,
		sessionTTL:       15 * time.Minute,
		heartbeatMaxAge:  30 * time.Second,
		suspectAfter:     60 * time.Second,
		emergencyStreams: make(map[string]chan *operatorv1.EmergencyCommand),
	}, nil
}

// SetInventorySyncer attaches an inventory syncer for release inventory sync (REQ-017).
func (s *Service) SetInventorySyncer(syncer *InventorySyncer) {
	s.inventorySyncer = syncer
}

// SetCommandExecutor attaches a runtime command executor for preflight execution (REQ-048).
func (s *Service) SetCommandExecutor(executor CommandExecutor) {
	s.commandExecutor = executor
}

// DispatchEmergency sends one validated emergency command to the active Operator stream.
func (s *Service) DispatchEmergency(
	ctx context.Context,
	req *connect.Request[operatorv1.DispatchEmergencyRequest],
) (*connect.Response[operatorv1.DispatchEmergencyResponse], error) {
	if req.Msg.GetOperatorId() == "" || req.Msg.GetCommand() == nil || req.Msg.GetCommand().GetCommandId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("operator_id and emergency command are required"))
	}
	s.streamMu.RLock()
	delivery := s.emergencyStreams[req.Msg.GetOperatorId()]
	s.streamMu.RUnlock()
	if delivery == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("operator stream is offline"))
	}
	select {
	case delivery <- req.Msg.GetCommand():
		return connect.NewResponse(&operatorv1.DispatchEmergencyResponse{}), nil
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeOf(ctx.Err()), ctx.Err())
	}
}

// Enroll validates a single-use enrollment token, creates an operator record,
// and establishes a new session for the operator agent.
//
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

	// Generate operator ID and cert serial.
	certSerial := hashBytes(certDER)
	if len(certSerial) > 10 {
		certSerial = certSerial[:10]
	}

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

	// Check operator is still active (AC-015-03).
	op, err := s.store.Operators().Get(ctx, operatorID)
	if err != nil {
		if err == store.ErrNotFound {
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("operator not found"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("get operator: %w", err))
	}
	switch op.Status {
	case store.OperatorSuperseded:
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("operator superseded: re-enroll required"))
	case store.OperatorRevoked:
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("operator revoked"))
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
	emergencyCh := make(chan *operatorv1.EmergencyCommand, 8)
	s.streamMu.Lock()
	s.emergencyStreams[operatorID] = emergencyCh
	s.streamMu.Unlock()
	defer func() {
		s.streamMu.Lock()
		delete(s.emergencyStreams, operatorID)
		s.streamMu.Unlock()
	}()

	go s.deliverPending(ctx, operatorID, deliverCh, deliverDone)

	for {
		select {
		case <-hbTicker.C:
			if err := s.store.Sessions().Heartbeat(ctx, sessionID); err != nil {
				s.logger.Warn("heartbeat failed", "error", err)
				if statusErr := s.store.Sessions().UpdateStatus(ctx, sessionID, store.SessionOffline); statusErr != nil {
					s.logger.Warn("failed to mark session offline", "error", statusErr)
				}
				return nil
			}

		case command := <-emergencyCh:
			if err := stream.Send(&operatorv1.CommandStreamResponse{
				Payload: &operatorv1.CommandStreamResponse_EmergencyCommand{EmergencyCommand: command},
			}); err != nil {
				return err
			}
			if intent, getErr := s.store.EmergencyIntents().GetByCommandID(ctx, command.GetCommandId()); getErr == nil {
				if updateErr := s.store.EmergencyIntents().UpdateDeliveryStatus(ctx, intent.ID, "delivered"); updateErr != nil {
					s.logger.Warn("failed to mark emergency delivered", "command_id", command.GetCommandId(), "error", updateErr)
				}
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
			if err := s.store.Outbox().UpdateStatus(ctx, entry.ID, store.CommandDelivered, ""); err != nil {
				return fmt.Errorf("mark command delivered: %w", err)
			}
		case <-ctx.Done():
			return nil

		default:
			req, err := stream.Receive()
			if err != nil {
				return err
			}

			switch {
			case req.GetHeartbeat() != nil:
				if err := s.store.Sessions().Heartbeat(ctx, sessionID); err != nil {
					s.logger.Warn("heartbeat update failed", "error", err)
				}

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
					if err := s.store.Outbox().UpdateStatus(ctx, ack.GetOutboxId(), store.CommandPersisted, ""); err != nil {
						s.logger.Warn("failed to mark command persisted", "error", err)
					}
					if entry, getErr := s.store.Outbox().Get(ctx, ack.GetOutboxId()); getErr == nil && entry.OperationType == "INVENTORY_SYNC" {
						if updateErr := s.store.InventorySyncRequests().UpdateStatus(ctx, entry.OperationID, store.InventorySyncRunning, ""); updateErr != nil {
							s.logger.Warn("failed to mark inventory sync running", "error", updateErr)
						}
					}
				}

			case req.GetEmergencyAck() != nil:
				ack := req.GetEmergencyAck()
				if ack.GetAckType() == operatorv1.AckType_ACK_TYPE_PERSISTED {
					if intent, getErr := s.store.EmergencyIntents().GetByCommandID(ctx, ack.GetEmergencyCommandId()); getErr == nil {
						if updateErr := s.store.EmergencyIntents().UpdateDeliveryStatus(ctx, intent.ID, "persisted"); updateErr != nil {
							s.logger.Warn("failed to mark emergency persisted", "command_id", ack.GetEmergencyCommandId(), "error", updateErr)
						}
						if op, opErr := s.store.Operations().Get(ctx, intent.OperationID); opErr == nil && op.Status == store.StatusQueued {
							if _, updateErr := s.store.Operations().UpdateStatus(ctx, op.ID, store.StatusRunning, op.StateVersion, ""); updateErr != nil {
								s.logger.Warn("failed to mark emergency running", "operation_id", op.ID, "error", updateErr)
							}
						}
					}
				}
			case req.GetEmergencyResult() != nil:
				s.finishEmergencyResult(ctx, req.GetEmergencyResult())
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
				requestStatus := store.InventorySyncSucceeded
				if result.GetStatus() == "failed" {
					status = store.CommandFailed
					requestStatus = store.InventorySyncFailed
				}
				resultJSON := result.GetResultJson()
				if resultJSON == "" {
					resultJSON = result.GetMessage()
				}
				if err := s.store.Outbox().UpdateStatus(ctx, result.GetOutboxId(), status, resultJSON); err != nil {
					s.logger.Warn("failed to persist command result", "error", err)
				}
				if existing != nil && existing.OperationType == "INVENTORY_SYNC" {
					if err := s.store.InventorySyncRequests().UpdateStatus(ctx, existing.OperationID, requestStatus, result.GetMessage()); err != nil {
						s.logger.Warn("failed to persist inventory sync result", "error", err)
					}
				} else if existing != nil {
					s.FinishOperation(ctx, existing.OperationID, result.GetStatus(), resultJSON)
				}

			case req.GetResyncResponse() != nil:
				rr := req.GetResyncResponse()
				s.logger.Info("operator resync response",
					"operator_last_sequence", rr.GetOperatorLastSequence(),
				)
				if err := s.reDeliverFrom(ctx, stream, operatorID, rr.GetOperatorLastSequence()); err != nil {
					return err
				}
			}
		}
	}
}

func (s *Service) finishEmergencyResult(ctx context.Context, result *operatorv1.EmergencyResult) {
	intent, err := s.store.EmergencyIntents().GetByCommandID(ctx, result.GetEmergencyCommandId())
	if err != nil {
		s.logger.Warn("failed to load emergency intent result", "command_id", result.GetEmergencyCommandId(), "error", err)
		return
	}
	var snapshots struct {
		Before json.RawMessage `json:"before"`
		After  json.RawMessage `json:"after"`
	}
	if result.GetResultJson() != "" {
		if err := json.Unmarshal([]byte(result.GetResultJson()), &snapshots); err != nil {
			s.logger.Warn("failed to decode emergency snapshots", "command_id", result.GetEmergencyCommandId(), "error", err)
		}
	}
	if err := s.store.EmergencyIntents().UpdateResult(ctx, intent.ID, snapshots.Before, snapshots.After); err != nil {
		s.logger.Warn("failed to persist emergency snapshots", "command_id", result.GetEmergencyCommandId(), "error", err)
	}
	op, err := s.store.Operations().Get(ctx, intent.OperationID)
	if err != nil {
		s.logger.Warn("failed to load emergency operation result", "operation_id", intent.OperationID, "error", err)
		return
	}
	status := store.StatusSucceeded
	lastError := ""
	if result.GetStatus() == "failed" {
		status = store.StatusFailed
		lastError = result.GetErrorCode()
		if lastError == "" {
			lastError = result.GetMessage()
		}
	}
	if _, err := s.store.Operations().UpdateStatus(ctx, op.ID, status, op.StateVersion, lastError); err != nil {
		s.logger.Warn("failed to finish emergency operation", "operation_id", op.ID, "error", err)
	}
}

// FinishOperation advances a queued/running operation to its terminal result.
func (s *Service) FinishOperation(ctx context.Context, operationID, resultStatus, resultJSON string) {
	op, err := s.store.Operations().Get(ctx, operationID)
	if err != nil {
		s.logger.Warn("failed to load operation for command result", "operation_id", operationID, "error", err)
		return
	}

	current := op.Status
	if current == store.StatusQueued {
		current, err = operation.Transition(current, operation.EventBegin)
		if err == nil {
			op, err = s.store.Operations().UpdateStatus(ctx, op.ID, current, op.StateVersion, "")
		}
		if err != nil {
			s.logger.Warn("failed to begin operation", "operation_id", operationID, "error", err)
			return
		}
	}

	event := operation.EventComplete
	lastError := ""
	if resultStatus == "failed" {
		event = operation.EventError
		lastError = resultJSON
	}
	next, err := operation.Transition(current, event)
	if err != nil {
		s.logger.Warn("failed to transition operation result", "operation_id", operationID, "error", err)
		return
	}
	if _, err := s.store.Operations().UpdateStatus(ctx, op.ID, next, op.StateVersion, lastError); err != nil {
		s.logger.Warn("failed to persist operation result", "operation_id", operationID, "error", err)
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
		if err := s.store.Outbox().UpdateStatus(ctx, entry.ID, store.CommandDelivered, ""); err != nil {
			return fmt.Errorf("mark re-delivered command: %w", err)
		}
	}
	return nil
}

// sendCommand serializes an outbox entry into a CommandStreamResponse.
func (s *Service) sendCommand(
	stream *connect.BidiStream[operatorv1.CommandStreamRequest, operatorv1.CommandStreamResponse],
	entry *store.OutboxEntry,
) error {
	command := &operatorv1.Command{
		OutboxId:      entry.ID,
		CommandId:     entry.CommandID,
		OperationId:   entry.OperationID,
		OperationType: entry.OperationType,
		Sequence:      entry.Sequence,
	}
	if err := DecodeCommandPayload(entry.Payload, command); err != nil {
		return fmt.Errorf("decode command payload %q: %w", entry.CommandID, err)
	}
	return stream.Send(&operatorv1.CommandStreamResponse{
		Payload: &operatorv1.CommandStreamResponse_Command{Command: command},
	})
}

type commandPayload struct {
	DefinitionID            string                  `json:"definition_id"`
	Namespace               string                  `json:"namespace"`
	ReleaseName             string                  `json:"release_name"`
	CreateNamespace         bool                    `json:"create_namespace"`
	TimeoutSeconds          int64                   `json:"timeout_seconds"`
	Bundle                  *commonv1.ReleaseBundle `json:"bundle"`
	Values                  json.RawMessage         `json:"values"`
	ValuesRevisionID        string                  `json:"values_revision_id"`
	ExpectedCurrentRevision int64                   `json:"expected_current_revision"`
	TargetRevision          int64                   `json:"target_revision"`
	Atomic                  bool                    `json:"atomic"`
	ValuesPatch             []byte                  `json:"values_patch"`
}

// DecodeCommandPayload populates command fields from an outbox JSON payload.
func DecodeCommandPayload(payload []byte, command *operatorv1.Command) error {
	if len(payload) == 0 {
		return nil
	}

	var envelope commandPayload
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("unmarshal envelope: %w", err)
	}
	command.DefinitionId = envelope.DefinitionID
	command.Namespace = envelope.Namespace
	command.ReleaseName = envelope.ReleaseName
	command.CreateNamespace = envelope.CreateNamespace
	command.TimeoutSeconds = envelope.TimeoutSeconds
	command.Bundle = envelope.Bundle
	command.Values = envelope.Values
	command.ValuesRevisionId = envelope.ValuesRevisionID
	command.ExpectedCurrentRevision = envelope.ExpectedCurrentRevision
	command.TargetRevision = envelope.TargetRevision
	command.Atomic = envelope.Atomic
	command.ValuesPatch = envelope.ValuesPatch

	if envelope.Bundle == nil {
		var bundle commonv1.ReleaseBundle
		if err := protojson.Unmarshal(payload, &bundle); err == nil && bundle.GetChartRef() != "" {
			command.Bundle = &bundle
		}
	}
	return nil
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
				if err := s.store.Outbox().UpdateSequence(ctx, entry.ID, seq); err != nil {
					s.logger.Warn("failed to persist command sequence", "error", err)
					continue
				}
			}

			select {
			case deliverCh <- entry:
			case <-done:
				return
			}
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

// hashBytes returns a hex-encoded SHA-256 of the input.
func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Compile-time check: Service implements the Connect handler interface.
var _ operatorv1connect.OperatorServiceHandler = (*Service)(nil)
