// Package operator implements the operator agent Connect service.
package operator

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/operator/commandtype"
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
	auditEmitter     audit.Sink
	streamMu         sync.RWMutex
	emergencyStreams map[string]chan *operatorv1.EmergencyCommand
	streams          *StreamRegistry
}

// NewService creates a new operator Connect service with a self-signed CA.
func NewService(st store.Store, logger *slog.Logger, opts ...Option) (*Service, error) {
	caInst, err := ca.New(ca.Config{TTL: 7 * 24 * time.Hour})
	if err != nil {
		return nil, fmt.Errorf("create CA: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	svc := &Service{
		store:            st,
		ca:               caInst,
		logger:           logger,
		sessionTTL:       15 * time.Minute,
		heartbeatMaxAge:  30 * time.Second,
		suspectAfter:     60 * time.Second,
		emergencyStreams: make(map[string]chan *operatorv1.EmergencyCommand),
		streams:          NewStreamRegistry(),
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc, nil
}

// Option configures a Service after construction.
type Option func(*Service)

// WithCA overrides the service CA. The gateway wiring shares one persisted CA
// between the listener trust anchor and certificate signing (TASK-075).
func WithCA(caInst *ca.CA) Option {
	return func(s *Service) {
		if caInst != nil {
			s.ca = caInst
		}
	}
}

// WithAudit attaches an audit sink to the service.
func WithAudit(sink audit.Sink) Option {
	return func(s *Service) {
		s.auditEmitter = sink
	}
}

// WithStreamRegistry overrides the command stream registry used to propagate
// session revocation to live streams (REQ-053).
func WithStreamRegistry(registry *StreamRegistry) Option {
	return func(s *Service) {
		if registry != nil {
			s.streams = registry
		}
	}
}

// SetInventorySyncer attaches an inventory syncer for release inventory sync (REQ-017).
func (s *Service) SetInventorySyncer(syncer *InventorySyncer) {
	s.inventorySyncer = syncer
}

// SetCommandExecutor attaches a runtime command executor for preflight execution (REQ-048).
func (s *Service) SetCommandExecutor(executor CommandExecutor) {
	s.commandExecutor = executor
}

// StreamRegistry returns the active command stream registry.
func (s *Service) StreamRegistry() *StreamRegistry { return s.streams }

// DispatchEmergency sends one validated emergency command to the active Operator stream.
func (s *Service) DispatchEmergency(ctx context.Context, operatorID string, command *operatorv1.EmergencyCommand) error {
	if operatorID == "" || command == nil || command.GetCommandId() == "" {
		return fmt.Errorf("operator_id and emergency command are required")
	}
	s.streamMu.RLock()
	delivery := s.emergencyStreams[operatorID]
	s.streamMu.RUnlock()
	if delivery == nil {
		return fmt.Errorf("operator stream is offline")
	}
	select {
	case delivery <- command:
		return nil
	case <-ctx.Done():
		return ctx.Err()
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

	if token.State != store.TokenStatePending {
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
	if existingByName, err := s.store.Operators().GetActiveByName(ctx, msg.GetCustomerId(), operatorName); err == nil {
		if existingByName.ClusterID != msg.GetClusterId() {
			return nil, connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("operator_name %q already registered on cluster %q", operatorName, existingByName.ClusterID))
		}
	}

	// Sign CSR with CA (AC-015-05).
	certDER, err := s.ca.SignCSR(csr)
	if err != nil {
		s.logger.Error("CA sign CSR failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign CSR: %w", err))
	}
	certPEM := ca.CertDERToPEM(certDER)

	// Generate operator ID and cert serial. The serial is the first 10 bytes
	// of sha256(certDER), hex-encoded (ADR-018; REQ-015 certificate contract).
	certSerial := certSerialFromDER(certDER)

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

	// Atomically consume the token, supersede any active identity, create the
	// new Operator, and establish its Session in the shared control-plane Store.
	now := time.Now().UTC()
	session := &store.Session{
		ID:            uuid.New().String(),
		OperatorID:    operatorID,
		CustomerID:    msg.GetCustomerId(),
		ClusterID:     msg.GetClusterId(),
		Status:        store.SessionOnline,
		StartedAt:     now,
		LastHeartbeat: now,
		ExpiresAt:     now.Add(s.sessionTTL),
		Capabilities:  msg.GetCapabilities(),
	}
	enrollment, err := s.store.OperatorManagement().EnrollOperator(ctx, token.ID, op, session)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("commit operator enrollment: %w", err))
	}
	if enrollment.SupersededOperatorID != "" {
		s.streams.Revoke(enrollment.SupersededOperatorID, "operator superseded")
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

	// ── Gateway mTLS path (TASK-075): identity comes from the client
	// certificate; a fresh session is established via SessionStore.Establish. ──
	if tlsState := TLSStateFromContext(ctx); tlsState != nil {
		if len(tlsState.PeerCertificates) == 0 {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("client certificate required on gateway"))
		}
		clientCert := tlsState.PeerCertificates[0]
		clusterID, customerID, ok := parseSANIdentity(clientCert)
		if !ok {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("client certificate SAN does not encode operator identity"))
		}

		op, err := s.store.Operators().GetByClusterID(ctx, clusterID)
		if err != nil {
			if err == store.ErrNotFound {
				return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("no operator registered for cluster %q", clusterID))
			}
			return connect.NewError(connect.CodeInternal, fmt.Errorf("get operator by cluster: %w", err))
		}
		if op.CustomerID != customerID {
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("certificate identity does not match operator"))
		}
		switch op.Status {
		case store.OperatorSuperseded:
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("operator superseded: re-enroll required"))
		case store.OperatorRevoked:
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("operator revoked"))
		}
		if op.CertSerial != certSerialFromCert(clientCert) {
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("certificate serial does not match registered operator"))
		}
		operatorID = op.ID

		now := time.Now().UTC()
		sess := &store.Session{
			ID:            uuid.New().String(),
			OperatorID:    operatorID,
			Status:        store.SessionOnline,
			InstanceID:    hello.GetInstanceId(),
			Version:       hello.GetVersion(),
			Capabilities:  hello.GetCapabilities(),
			StartedAt:     now,
			LastHeartbeat: now,
			ExpiresAt:     now.Add(s.sessionTTL),
		}
		// The Enroll placeholder session (empty InstanceID) does not block the
		// first Establish — it is replaced, not treated as a concurrent
		// connection (store semantics, REQ-044 D-57: Hello takes over session
		// establishment and the fresh session_id is authoritative).
		if err := s.store.Sessions().Establish(ctx, sess); err != nil {
			if err == store.ErrDuplicateKey {
				return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("another session for this operator is already online"))
			}
			return connect.NewError(connect.CodeInternal, fmt.Errorf("establish session: %w", err))
		}
		sessionID = sess.ID

		s.logger.Info("operator stream established via mTLS",
			"operator_id", operatorID,
			"session_id", sessionID,
			"cluster_id", clusterID,
			"customer_id", customerID,
			"last_seen_sequence", lastSeenSeq,
		)
	} else {
		// ── Plain path (management plane / legacy dev agents): existing
		// session_id validation. TASK-065 removes this path. ──
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
	}

	s.logger.Info("operator stream established",
		"operator_id", operatorID, "session_id", sessionID,
		"last_seen_sequence", lastSeenSeq,
	)
	streamCtx, cancelStream := context.WithCancel(ctx)
	unregisterStream := s.streams.Register(operatorID, sessionID, cancelStream)
	defer unregisterStream()
	ctx = streamCtx

	// First response is always SessionEstablished (REQ-044 output contract;
	// the bootstrap agent waits for it before entering the main loop).
	if err := stream.Send(&operatorv1.CommandStreamResponse{
		Payload: &operatorv1.CommandStreamResponse_SessionEstablished{
			SessionEstablished: &operatorv1.SessionEstablished{
				SessionId:                sessionID,
				HeartbeatIntervalSeconds: int64((s.heartbeatMaxAge / 2) / time.Second),
				HeartbeatTimeoutSeconds:  int64(s.suspectAfter / time.Second),
			},
		},
	}); err != nil {
		return err
	}

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

	requestCh := make(chan *operatorv1.CommandStreamRequest)
	receiveErrCh := make(chan error, 1)
	go func() {
		defer close(requestCh)
		for {
			request, receiveErr := stream.Receive()
			if receiveErr != nil {
				receiveErrCh <- receiveErr
				return
			}
			select {
			case requestCh <- request:
			case <-ctx.Done():
				receiveErrCh <- ctx.Err()
				return
			}
		}
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
			if op, err := s.store.Operators().Get(context.WithoutCancel(ctx), operatorID); err == nil && op.Status == store.OperatorRevoked {
				return connect.NewError(connect.CodePermissionDenied, errors.New("operator revoked"))
			}
			return nil

		case req, ok := <-requestCh:
			if !ok {
				return <-receiveErrCh
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
				// but only ACK_PERSISTED releases the orchestrator from
				// re-delivery responsibility.
				if ack.GetAckType() == operatorv1.AckType_ACK_TYPE_PERSISTED {
					if entry, err := s.store.Outbox().PersistAck(ctx, ack.GetOutboxId()); err != nil {
						s.logger.Warn("failed to persist command ack", "error", err)
					} else if entry != nil {
						s.logger.Debug("command ack persisted with timeline entry",
							"outbox_id", ack.GetOutboxId(),
							"sequence", entry.Sequence,
						)
					} else {
						s.logger.Debug("command ack replay, already persisted", "outbox_id", ack.GetOutboxId())
					}
				}

			case req.GetEmergencyAck() != nil:
				ack := req.GetEmergencyAck()
				if ack.GetAckType() == operatorv1.AckType_ACK_TYPE_PERSISTED {
					if intent, getErr := s.store.EmergencyIntents().GetByCommandID(ctx, ack.GetEmergencyCommandId()); getErr == nil {
						if entry, ackErr := s.store.EmergencyIntents().PersistAck(ctx, intent.ID); ackErr != nil {
							s.logger.Warn("failed to persist emergency ack", "command_id", ack.GetEmergencyCommandId(), "error", ackErr)
						} else if entry != nil {
							s.logger.Debug("emergency ack persisted with timeline entry",
								"command_id", ack.GetEmergencyCommandId(),
								"sequence", entry.Sequence,
							)
						} else {
							s.logger.Debug("emergency ack replay, already persisted", "command_id", ack.GetEmergencyCommandId())
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
				existing, err := s.store.Outbox().GetByCommandID(ctx, result.GetCommandId())
				if err != nil && err != store.ErrNotFound {
					s.logger.Warn("failed to check command dedup", "error", err)
				}
				if existing != nil && (existing.Status == store.CommandSucceeded || existing.Status == store.CommandFailed) {
					if err := stream.Send(&operatorv1.CommandStreamResponse{
						Payload: &operatorv1.CommandStreamResponse_DuplicateResponse{
							DuplicateResponse: &operatorv1.DuplicateResponse{CommandId: result.GetCommandId(), ResultJson: existing.ResultJSON},
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
				if existing != nil && existing.OperationType == commandtype.InventorySync {
					if err := s.store.InventorySyncRequests().UpdateStatus(ctx, existing.OperationID, requestStatus, result.GetMessage()); err != nil {
						s.logger.Warn("failed to persist inventory sync result", "error", err)
					}
				} else if existing != nil && existing.OperationType != commandtype.SecretMetadataList {
					s.FinishOperation(ctx, existing.OperationID, result.GetStatus(), resultJSON)
				}

			case req.GetCommandResult() != nil:
				result := req.GetCommandResult()
				entry, err := s.store.Outbox().GetByCommandID(ctx, result.GetCommandId())
				if err != nil {
					return fmt.Errorf("load typed command result outbox: %w", err)
				}
				if err := s.HandleCommandResult(ctx, result); err != nil {
					return err
				}
				payload, err := protojson.Marshal(result)
				if err != nil {
					return fmt.Errorf("marshal typed command result: %w", err)
				}
				status := store.CommandSucceeded
				if result.GetStatus() != "succeeded" {
					status = store.CommandFailed
				}
				if err := s.store.Outbox().UpdateStatus(ctx, entry.ID, status, string(payload)); err != nil {
					return fmt.Errorf("persist typed command result outbox: %w", err)
				}

			case req.GetResyncResponse() != nil:
				rr := req.GetResyncResponse()
				if err := s.reDeliverFrom(ctx, stream, operatorID, rr.GetOperatorLastSequence()); err != nil {
					return err
				}
			}
		case receiveErr := <-receiveErrCh:
			return receiveErr
		}
	}
}

func (s *Service) finishEmergencyResult(ctx context.Context, result *operatorv1.EmergencyResult) {
	intent, err := s.store.EmergencyIntents().GetByCommandID(ctx, result.GetEmergencyCommandId())
	if err != nil {
		s.logger.Warn("failed to load emergency intent result", "command_id", result.GetEmergencyCommandId(), "error", err)
		return
	}
	op, err := s.store.Operations().Get(ctx, intent.OperationID)
	if err != nil {
		s.logger.Warn("failed to load emergency operation result", "operation_id", intent.OperationID, "error", err)
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
	if op.Status.IsTerminal() {
		effectStatus := store.EmergencyEffectApplied
		if result.GetStatus() == "failed" {
			effectStatus = store.EmergencyEffectNotApplied
		}
		resolved, resolveErr := s.store.EmergencyIntents().ResolveEmergencyEffect(ctx, store.ResolveEmergencyEffectCommand{
			OperationID: op.ID, ExpectedStateVersion: op.StateVersion, EffectStatus: effectStatus,
			BeforeSnapshot: snapshots.Before, AfterSnapshot: snapshots.After, RequestID: result.GetEmergencyCommandId(),
		})
		if resolveErr != nil {
			s.logger.Warn("failed to resolve late emergency effect", "operation_id", op.ID, "error", resolveErr)
			return
		}
		if resolved.Resolved {
			s.logger.Info("resolved late emergency effect", "operation_id", op.ID, "effect_status", effectStatus)
		}
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
	effectStatus := store.EmergencyEffectApplied
	if status == store.StatusFailed {
		effectStatus = store.EmergencyEffectNotApplied
	}
	finished, err := s.store.EmergencyIntents().Finish(
		ctx,
		intent.ID,
		op.ID,
		op.StateVersion,
		status,
		effectStatus,
		lastError,
		snapshots.Before,
		snapshots.After,
	)
	if err != nil {
		s.logger.Warn("failed to finish emergency operation", "operation_id", op.ID, "error", err)
		return
	}
	s.emitEmergencyTerminalAudit(finished, intent, snapshots.Before, snapshots.After, result.GetErrorCode())
}

func (s *Service) emitEmergencyTerminalAudit(
	op *store.Operation,
	intent *store.EmergencyIntent,
	before, after json.RawMessage,
	errorCode string,
) {
	if s.auditEmitter == nil || op == nil || intent == nil {
		return
	}
	status := "succeeded"
	if op.Status != store.StatusSucceeded {
		status = string(op.Status)
	}
	changeSummary, err := json.Marshal(map[string]any{
		"action": intent.Action,
		"before": before,
		"after":  after,
	})
	if err != nil {
		s.logger.Warn("failed to encode emergency audit summary", "operation_id", op.ID, "error", err)
		return
	}
	event := audit.NewEvent(
		store.AuditActorUser,
		op.Actor.UserID,
		op.Actor.Organization,
		"",
		"operation",
		op.ID,
		"emergency_change",
		status,
		string(changeSummary),
		map[string]string{
			"definition_id": intent.ReleaseDefinitionID,
			"payload_hash":  op.RequestHash,
			"error_code":    errorCode,
		},
	)
	if s.auditEmitter != nil {
		if result := s.auditEmitter.Emit(event); !result.Accepted {
			s.logger.Warn("emergency audit event rejected", "operation_id", op.ID, "code", result.Code)
		}
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
	if current == store.StatusPending {
		if resultStatus == "failed" {
			if _, err := s.store.Operations().UpdateStatus(ctx, op.ID, store.StatusFailed, op.StateVersion, resultJSON); err != nil {
				s.logger.Warn("failed to persist pending preflight failure", "operation_id", operationID, "error", err)
			}
			return
		}
		s.logger.Warn("ignoring successful command result for pending operation", "operation_id", operationID)
		return
	}
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

// HandleCommandResult atomically applies one typed Upgrade result.
func (s *Service) HandleCommandResult(ctx context.Context, result *operatorv1.CommandResult) error {
	if result == nil || result.GetOperationId() == "" || result.GetCommandId() == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("command_result identifiers are required"))
	}
	op, err := s.store.Operations().Get(ctx, result.GetOperationId())
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("load operation for result: %w", err))
	}
	if op.Status.IsTerminal() {
		return nil
	}
	if op.Status == store.StatusQueued {
		op, err = s.store.Operations().UpdateStatus(ctx, op.ID, store.StatusRunning, op.StateVersion, "")
		if err != nil {
			return connect.NewError(connect.CodeAborted, fmt.Errorf("begin operation: %w", err))
		}
	}
	definition, err := s.store.Definitions().Get(ctx, op.ReleaseDefinitionID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("load release definition for result: %w", err))
	}
	upgrade := result.GetUpgrade()
	if upgrade == nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upgrade result is required"))
	}
	active := upgrade.GetActive()
	updateInventory := active != nil
	if active == nil && result.GetStatus() == "succeeded" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("successful upgrade result requires active snapshot"))
	}
	status, lastError, inventoryStatus := terminalMapping(result)
	payload, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(upgrade)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("marshal upgrade result: %w", err))
	}
	if err := s.store.UpgradeResults().FinalizeUpgrade(ctx, &store.UpgradeTerminalInput{
		OperationID:                   op.ID,
		ExpectedStateVersion:          op.StateVersion,
		Status:                        status,
		LastError:                     lastError,
		ResultPayload:                 payload,
		ReleaseDefinitionID:           op.ReleaseDefinitionID,
		CustomerID:                    definition.CustomerID,
		ClusterID:                     definition.ClusterID,
		UpdateInventory:               updateInventory,
		Revision:                      int(active.GetHelmRevision()), //nolint:gosec // Helm revisions are bounded by the signed int used by the SDK and store.
		ObservedBundleDigest:          active.GetBundleDigest(),
		ObservedChartDigest:           active.GetChartDigest(),
		ObservedEffectiveValuesDigest: active.GetEffectiveValuesDigest(),
		ObservedManifestDigest:        active.GetManifestDigest(),
		LiveStatus:                    active.GetStatus(),
		InventoryStatus:               inventoryStatus,
		ResourceCount:                 int(upgrade.GetResourceSummary().GetResourceCount()),
	}); err != nil {
		if errors.Is(err, store.ErrOptimisticLock) {
			return connect.NewError(connect.CodeAborted, err)
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("finalize upgrade result: %w", err))
	}
	return nil
}

func terminalMapping(result *operatorv1.CommandResult) (store.OperationStatus, string, store.InventoryStatus) {
	if result.GetStatus() == "succeeded" {
		return store.StatusSucceeded, "", store.InventoryActive
	}
	if result.GetError().GetCode() == "atomic_rollback_failed" {
		return store.StatusFailed, result.GetError().GetCode(), store.InventoryOutOfSync
	}
	if result.GetError().GetCode() == "helm_cancelled" {
		return store.StatusCancelled, result.GetError().GetCode(), store.InventoryActive
	}
	if result.GetError().GetCode() == "helm_timeout" {
		return store.StatusTimeout, result.GetError().GetCode(), store.InventoryActive
	}
	return store.StatusFailed, result.GetError().GetCode(), store.InventoryActive
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
	DefinitionID            string                     `json:"definition_id"`
	Namespace               string                     `json:"namespace"`
	ReleaseName             string                     `json:"release_name"`
	CreateNamespace         bool                       `json:"create_namespace"`
	TimeoutSeconds          int64                      `json:"timeout_seconds"`
	Bundle                  *commonv1.ReleaseBundle    `json:"bundle"`
	Values                  json.RawMessage            `json:"values"`
	ValuesRevisionID        string                     `json:"values_revision_id"`
	ExpectedCurrentRevision int64                      `json:"expected_current_revision"`
	TargetRevision          int64                      `json:"target_revision"`
	Atomic                  bool                       `json:"atomic"`
	ValuesPatch             json.RawMessage            `json:"values_patch"`
	PayloadVersion          uint32                     `json:"payload_version"`
	Upgrade                 *operatorv1.UpgradeCommand `json:"upgrade"`
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
	command.ValuesPatch = []byte(envelope.ValuesPatch)
	command.PayloadVersion = envelope.PayloadVersion
	if envelope.Upgrade != nil {
		command.TypedPayload = &operatorv1.Command_Upgrade{Upgrade: envelope.Upgrade}
	}

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
				if err := s.store.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPending, ""); err != nil {
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

	status := "already_revoked"
	if op.Status != store.OperatorRevoked {
		if _, err := s.store.OperatorManagement().RevokeOperator(ctx, op.CustomerID, op.ClusterID, op.ID, msg.GetReason(), nil); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("revoke operator: %w", err))
		}
		status = "revoked"
	}
	s.streams.Revoke(op.ID, "operator revoked")
	s.logger.Warn("operator revoked", "operator_id", op.ID, "reason_provided", strings.TrimSpace(msg.GetReason()) != "")
	return connect.NewResponse(&operatorv1.RevokeOperatorResponse{
		OperatorId: op.ID,
		Status:     status,
	}), nil
}

// certSerialFromCert computes the ADR-018 identity serial: the first 10 bytes
// of the SHA-256 digest of the DER certificate, hex-encoded (20 hex chars, 80
// bits). This is the same derivation Enroll uses for Operator.CertSerial.
func certSerialFromCert(cert *x509.Certificate) string {
	return certSerialFromDER(cert.Raw)
}

// certSerialFromDER derives the ADR-018 identity serial from a certificate's
// DER encoding: sha256(certDER) truncated to its first 10 bytes, lower-case
// hex (REQ-015 certificate contract; ADR-018).
func certSerialFromDER(certDER []byte) string {
	h := sha256.Sum256(certDER)
	return hex.EncodeToString(h[:10])
}

// parseSANIdentity extracts the cluster/customer identity from a gateway
// client certificate. The canonical SAN (REQ-015 decision 3) is
// `<cluster>.<customer>.rm`, lower-cased; cluster IDs may contain dots, so the
// customer is always the component immediately before the ".rm" suffix.
func parseSANIdentity(cert *x509.Certificate) (clusterID, customerID string, ok bool) {
	for _, name := range cert.DNSNames {
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".rm") {
			continue
		}
		trimmed := strings.TrimSuffix(lower, ".rm")
		parts := strings.Split(trimmed, ".")
		if len(parts) < 2 {
			continue
		}
		return strings.Join(parts[:len(parts)-1], "."), parts[len(parts)-1], true
	}
	return "", "", false
}

// Compile-time check: Service implements the Connect handler interface.
var _ operatorv1connect.OperatorServiceHandler = (*Service)(nil)
