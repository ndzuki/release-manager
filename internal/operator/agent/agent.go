// Package agent executes commands delivered by the operator CommandStream.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
)

const defaultRetryDelay = time.Second

// CommandStream is the subset of the generated Connect stream used by Agent.
// It keeps command execution tests independent from a live HTTP server.
type CommandStream interface {
	Send(*operatorv1.CommandStreamRequest) error
	Receive() (*operatorv1.CommandStreamResponse, error)
	CloseRequest() error
	CloseResponse() error
}

// Options configures an Agent.
type Options struct {
	Client        operatorv1connect.OperatorServiceClient
	Engine        helmengine.Engine
	Store         localstore.Store
	SessionID     string
	OperatorID    string
	Logger        *slog.Logger
	RetryDelay    time.Duration
	StreamFactory func(context.Context) CommandStream
	OnComplete    func(namespace, releaseName, operationID string)
}

// Agent receives and executes commands from an orchestrator.
type Agent struct {
	engine        helmengine.Engine
	store         localstore.Store
	sessionID     string
	operatorID    string
	logger        *slog.Logger
	retryDelay    time.Duration
	streamFactory func(context.Context) CommandStream
	onComplete    func(namespace, releaseName, operationID string)
}

// New creates an Agent with durable command de-duplication.
func New(opts Options) (*Agent, error) {
	if opts.Engine == nil {
		return nil, errors.New("helm engine is required")
	}
	if opts.Store == nil {
		return nil, errors.New("local command store is required")
	}
	if opts.SessionID == "" {
		return nil, errors.New("session id is required")
	}
	if opts.OperatorID == "" {
		return nil, errors.New("operator id is required")
	}
	if opts.StreamFactory == nil && opts.Client == nil {
		return nil, errors.New("operator service client is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = defaultRetryDelay
	}

	factory := opts.StreamFactory
	if factory == nil {
		factory = func(ctx context.Context) CommandStream {
			return opts.Client.CommandStream(ctx)
		}
	}

	return &Agent{
		engine:        opts.Engine,
		store:         opts.Store,
		sessionID:     opts.SessionID,
		operatorID:    opts.OperatorID,
		logger:        opts.Logger,
		retryDelay:    opts.RetryDelay,
		streamFactory: factory,
		onComplete:    opts.OnComplete,
	}, nil
}

// Run maintains the CommandStream connection until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	for {
		err := a.runStream(ctx)
		if ctx.Err() != nil {
			//nolint:nilerr // context cancellation is a clean agent shutdown.
			return nil
		}
		if err != nil {
			a.logger.Warn("operator command stream disconnected", "error", err)
		}

		timer := time.NewTimer(a.retryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func (a *Agent) runStream(ctx context.Context) error {
	stream := a.streamFactory(ctx)
	if stream == nil {
		return errors.New("command stream is nil")
	}
	defer func() {
		if err := stream.CloseRequest(); err != nil {
			a.logger.Warn("failed to close command request stream", "error", err)
		}
		if err := stream.CloseResponse(); err != nil {
			a.logger.Warn("failed to close command response stream", "error", err)
		}
	}()
	if err := stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{
				SessionId:        a.sessionID,
				OperatorId:       a.operatorID,
				LastSeenSequence: a.lastSeenSequence(ctx),
			},
		},
	}); err != nil {
		return fmt.Errorf("send command stream hello: %w", err)
	}

	for {
		response, err := stream.Receive()
		if err != nil {
			return err
		}
		command := response.GetCommand()
		if command == nil {
			continue
		}
		if err := a.handleCommand(ctx, stream, command); err != nil {
			return err
		}
	}
}

func (a *Agent) lastSeenSequence(ctx context.Context) int64 {
	sequence, err := a.store.LastSequence(ctx)
	if err != nil {
		a.logger.Warn("failed to read last command sequence", "error", err)
		return 0
	}
	return sequence
}

//nolint:gocyclo // command lifecycle combines durable deduplication, ACKs, execution, and result delivery.
func (a *Agent) handleCommand(
	ctx context.Context,
	stream CommandStream,
	command *operatorv1.Command,
) error {
	if command.GetCommandId() == "" {
		return errors.New("command id is required")
	}

	existing, err := a.store.Get(ctx, command.GetCommandId())
	if err != nil && !errors.Is(err, localstore.ErrNotFound) {
		return fmt.Errorf("load command %s: %w", command.GetCommandId(), err)
	}
	if existing != nil && localstore.IsTerminal(existing.Status) {
		return a.sendCachedResult(stream, command, existing)
	}

	payload, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("marshal command %s: %w", command.GetCommandId(), err)
	}
	entry := &localstore.CommandEntry{
		CommandID:   command.GetCommandId(),
		OutboxID:    command.GetOutboxId(),
		OperationID: command.GetOperationId(),
		Sequence:    command.GetSequence(),
		Payload:     payload,
		Status:      localstore.StatusPending,
	}
	if existing == nil {
		if err := a.store.Save(ctx, entry); err != nil {
			return fmt.Errorf("persist command %s: %w", command.GetCommandId(), err)
		}
	}

	if err := a.sendAck(stream, command, operatorv1.AckType_ACK_TYPE_RECEIVED); err != nil {
		return err
	}
	if err := a.sendAck(stream, command, operatorv1.AckType_ACK_TYPE_PERSISTED); err != nil {
		return err
	}
	if err := a.store.UpdateStatus(ctx, command.GetCommandId(), localstore.StatusRunning, ""); err != nil {
		return fmt.Errorf("mark command %s running: %w", command.GetCommandId(), err)
	}

	result := a.execute(ctx, command)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal command result %s: %w", command.GetCommandId(), err)
	}

	status := localstore.StatusSucceeded
	if result.Status == "failed" {
		status = localstore.StatusFailed
	}
	if err := a.store.UpdateStatus(ctx, command.GetCommandId(), status, string(resultJSON)); err != nil {
		return fmt.Errorf("persist command result %s: %w", command.GetCommandId(), err)
	}
	if result.Status == "succeeded" && a.onComplete != nil {
		a.onComplete(result.Namespace, result.ReleaseName, command.GetOperationId())
	}

	return stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Result{
			Result: &operatorv1.Result{
				OutboxId:   command.GetOutboxId(),
				CommandId:  command.GetCommandId(),
				Status:     result.Status,
				Message:    result.Message,
				Output:     resultJSON,
				Sequence:   command.GetSequence(),
				ResultJson: string(resultJSON),
			},
		},
	})
}

func (a *Agent) sendAck(
	stream CommandStream,
	command *operatorv1.Command,
	ackType operatorv1.AckType,
) error {
	return stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Ack{
			Ack: &operatorv1.Ack{
				OutboxId:  command.GetOutboxId(),
				CommandId: command.GetCommandId(),
				Sequence:  command.GetSequence(),
				AckType:   ackType,
			},
		},
	})
}

func (a *Agent) sendCachedResult(
	stream CommandStream,
	command *operatorv1.Command,
	entry *localstore.CommandEntry,
) error {
	var cached commandResult
	if err := json.Unmarshal([]byte(entry.ResultJSON), &cached); err != nil {
		return fmt.Errorf("decode cached result %s: %w", command.GetCommandId(), err)
	}
	return stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Result{
			Result: &operatorv1.Result{
				OutboxId:   command.GetOutboxId(),
				CommandId:  command.GetCommandId(),
				Status:     cached.Status,
				Message:    cached.Message,
				Output:     []byte(entry.ResultJSON),
				Sequence:   command.GetSequence(),
				ResultJson: entry.ResultJSON,
			},
		},
	})
}

type commandPayload struct {
	Namespace        string                 `json:"namespace"`
	ReleaseName      string                 `json:"release_name"`
	ChartPath        string                 `json:"chart_path"`
	Values           map[string]interface{} `json:"values"`
	ExpectedRevision int                    `json:"expected_revision"`
	Atomic           bool                   `json:"atomic"`
	Timeout          int                    `json:"timeout"`
}

type commandResult struct {
	Status      string              `json:"status"`
	Message     string              `json:"message,omitempty"`
	ErrorCode   string              `json:"error_code,omitempty"`
	OperationID string              `json:"operation_id"`
	Namespace   string              `json:"namespace,omitempty"`
	ReleaseName string              `json:"release_name,omitempty"`
	Release     *helmengine.Release `json:"release,omitempty"`
}

func (a *Agent) execute(ctx context.Context, command *operatorv1.Command) commandResult {
	payload, err := decodePayload(command)
	result := commandResult{
		OperationID: command.GetOperationId(),
		Status:      "failed",
	}
	if err != nil {
		result.ErrorCode = "invalid_command"
		result.Message = err.Error()
		return result
	}
	result.Namespace = payload.Namespace
	result.ReleaseName = payload.ReleaseName

	opCtx := ctx
	cancel := func() {}
	if payload.Timeout > 0 {
		opCtx, cancel = context.WithTimeout(ctx, time.Duration(payload.Timeout)*time.Second)
	}
	defer cancel()

	switch strings.ToUpper(command.GetOperationType()) {
	case "INSTALL":
		release, execErr := a.engine.Install(opCtx, helmengine.InstallOptions{
			Namespace:   payload.Namespace,
			ReleaseName: payload.ReleaseName,
			ChartPath:   payload.ChartPath,
			Values:      payload.Values,
		})
		if execErr != nil {
			result.ErrorCode = errorCode(execErr, "helm_install_failed")
			result.Message = execErr.Error()
			return result
		}
		result.Status = "succeeded"
		result.Release = release
	case "UPGRADE":
		release, execErr := a.engine.Upgrade(opCtx, helmengine.UpgradeOptions{
			Namespace:        payload.Namespace,
			ReleaseName:      payload.ReleaseName,
			ChartPath:        payload.ChartPath,
			Values:           payload.Values,
			ExpectedRevision: payload.ExpectedRevision,
			Atomic:           payload.Atomic,
			Timeout:          payload.Timeout,
		})
		if execErr != nil {
			result.ErrorCode = errorCode(execErr, "helm_upgrade_failed")
			result.Message = execErr.Error()
			return result
		}
		result.Status = "succeeded"
		result.Release = release
	case "ROLLBACK":
		release, execErr := a.engine.Rollback(opCtx, helmengine.RollbackOptions{
			Namespace:      payload.Namespace,
			ReleaseName:    payload.ReleaseName,
			TargetRevision: payload.ExpectedRevision,
		})
		if execErr != nil {
			result.ErrorCode = errorCode(execErr, "helm_rollback_failed")
			result.Message = execErr.Error()
			return result
		}
		result.Status = "succeeded"
		result.Release = release
	default:
		result.ErrorCode = "unsupported_operation"
		result.Message = fmt.Sprintf("unsupported operation type: %s", command.GetOperationType())
	}
	return result
}

func decodePayload(command *operatorv1.Command) (commandPayload, error) {
	if len(command.GetValues()) == 0 {
		return commandPayload{}, errors.New("command values payload is required")
	}
	var payload commandPayload
	if err := json.Unmarshal(command.GetValues(), &payload); err != nil {
		return commandPayload{}, fmt.Errorf("decode command values: %w", err)
	}
	if payload.ChartPath == "" && command.GetBundle() != nil {
		payload.ChartPath = command.GetBundle().GetChartRef()
	}
	if payload.Namespace == "" || payload.ReleaseName == "" || payload.ChartPath == "" {
		return commandPayload{}, errors.New("command payload requires namespace, release_name, and chart_path")
	}
	if payload.Values == nil {
		payload.Values = map[string]interface{}{}
	}
	return payload, nil
}

func errorCode(err error, fallback string) string {
	switch {
	case errors.Is(err, helmengine.ErrNotFound):
		return "release_not_found"
	case errors.Is(err, helmengine.ErrConflict):
		return "revision_conflict"
	case errors.Is(err, helmengine.ErrAlreadyExists):
		return "release_already_exists"
	case errors.Is(err, helmengine.ErrTimeout):
		return "timeout"
	default:
		return fallback
	}
}
