// Package agent executes commands received from the operator control stream.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
)

const defaultInstallTimeout = 5 * time.Minute

// Stream is the operator-side command stream contract used by Agent.
type Stream interface {
	Send(*operatorv1.CommandStreamRequest) error
	Receive() (*operatorv1.CommandStreamResponse, error)
	CloseRequest() error
	CloseResponse() error
}

// StreamClient creates operator command streams.
type StreamClient interface {
	CommandStream(context.Context) Stream
}

// InventoryNotifier schedules a targeted inventory update after a successful operation.
type InventoryNotifier interface {
	NotifyOperationComplete(namespace, releaseName, operationID, definitionID string)
}

// Agent receives durable commands, executes Helm operations, and returns cached results on redelivery.
type Agent struct {
	client       StreamClient
	engine       helmengine.Engine
	store        localstore.Store
	notifier     InventoryNotifier
	sessionID    string
	operatorID   string
	logger       *slog.Logger
	installFlags InstallFlags
}

// InstallFlags contains operator-wide defaults for INSTALL commands.
type InstallFlags struct {
	Atomic  bool
	Timeout time.Duration
}

// Config contains Agent dependencies and session identity.
type Config struct {
	Client       StreamClient
	Engine       helmengine.Engine
	Store        localstore.Store
	Notifier     InventoryNotifier
	SessionID    string
	OperatorID   string
	Logger       *slog.Logger
	InstallFlags InstallFlags
}

// Result is persisted locally and sent to the orchestrator for idempotent replay.
type Result struct {
	OperationID     string              `json:"operation_id"`
	CommandID       string              `json:"command_id"`
	DefinitionID    string              `json:"definition_id"`
	Status          string              `json:"status"`
	Code            string              `json:"code,omitempty"`
	Message         string              `json:"message,omitempty"`
	Release         *helmengine.Release `json:"release,omitempty"`
	InventorySync   bool                `json:"inventory_sync_hint"`
	ResourceSummary ResourceSummary     `json:"resource_summary"`
}

// ResourceSummary contains non-sensitive output metadata.
type ResourceSummary struct {
	ManifestDigest string `json:"manifest_digest,omitempty"`
}

// New creates an Agent.
func New(cfg Config) (*Agent, error) {
	switch {
	case cfg.Client == nil:
		return nil, errors.New("agent client is required")
	case cfg.Engine == nil:
		return nil, errors.New("agent engine is required")
	case cfg.Store == nil:
		return nil, errors.New("agent store is required")
	case cfg.SessionID == "":
		return nil, errors.New("agent session_id is required")
	case cfg.OperatorID == "":
		return nil, errors.New("agent operator_id is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.InstallFlags.Timeout <= 0 {
		cfg.InstallFlags.Timeout = defaultInstallTimeout
	}

	return &Agent{
		client:       cfg.Client,
		engine:       cfg.Engine,
		store:        cfg.Store,
		notifier:     cfg.Notifier,
		sessionID:    cfg.SessionID,
		operatorID:   cfg.OperatorID,
		logger:       logger,
		installFlags: cfg.InstallFlags,
	}, nil
}

// Run connects to CommandStream and processes commands until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	lastSequence, err := a.store.LastSequence(ctx)
	if err != nil {
		return fmt.Errorf("load last command sequence: %w", err)
	}

	stream := a.client.CommandStream(ctx)
	defer stream.CloseResponse() //nolint:errcheck // stream is already terminating

	if err := stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{
				SessionId:        a.sessionID,
				OperatorId:       a.operatorID,
				LastSeenSequence: lastSequence,
			},
		},
	}); err != nil {
		return fmt.Errorf("send operator hello: %w", err)
	}

	if err := a.replayActive(ctx, stream); err != nil {
		return err
	}

	for {
		response, err := stream.Receive()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("receive operator command: %w", err)
		}

		switch {
		case response.GetCommand() != nil:
			if err := a.handleCommand(ctx, stream, response.GetCommand()); err != nil {
				return err
			}
		case response.GetResyncRequest() != nil:
			lastSequence, err := a.store.LastSequence(ctx)
			if err != nil {
				return fmt.Errorf("load sequence for resync: %w", err)
			}
			if err := stream.Send(&operatorv1.CommandStreamRequest{
				Payload: &operatorv1.CommandStreamRequest_ResyncResponse{
					ResyncResponse: &operatorv1.ResyncResponse{OperatorLastSequence: lastSequence},
				},
			}); err != nil {
				return fmt.Errorf("send resync response: %w", err)
			}
		case response.GetDuplicateResponse() != nil:
			a.logger.Debug("received duplicate command result",
				"command_id", response.GetDuplicateResponse().GetCommandId(),
			)
		case response.GetSessionEvent() != nil:
			return fmt.Errorf("operator session %s: %s",
				response.GetSessionEvent().GetType(),
				response.GetSessionEvent().GetMessage(),
			)
		}
	}
}

func (a *Agent) replayActive(ctx context.Context, stream Stream) error {
	entries, err := a.store.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("list active commands: %w", err)
	}
	for _, entry := range entries {
		if err := a.executeEntry(ctx, stream, entry); err != nil {
			return fmt.Errorf("replay command %q: %w", entry.CommandID, err)
		}
	}
	return nil
}

func (a *Agent) handleCommand(ctx context.Context, stream Stream, command *operatorv1.Command) error {
	if command.GetCommandId() == "" {
		return errors.New("received command without command_id")
	}

	existing, err := a.store.Get(ctx, command.GetCommandId())
	if err == nil {
		if localstore.IsTerminal(existing.Status) {
			return a.sendCachedResult(stream, command, existing.ResultJSON)
		}
		return a.executeEntry(ctx, stream, existing)
	}
	if !errors.Is(err, localstore.ErrNotFound) {
		return fmt.Errorf("lookup command %q: %w", command.GetCommandId(), err)
	}

	payload, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("marshal command %q: %w", command.GetCommandId(), err)
	}
	entry := &localstore.CommandEntry{
		CommandID:     command.GetCommandId(),
		OutboxID:      command.GetOutboxId(),
		OperationID:   command.GetOperationId(),
		OperationType: command.GetOperationType(),
		Sequence:      command.GetSequence(),
		Payload:       payload,
		Status:        localstore.StatusPending,
	}
	if err := a.store.Save(ctx, entry); err != nil {
		return fmt.Errorf("persist command %q: %w", command.GetCommandId(), err)
	}

	if err := stream.Send(ackRequest(command, operatorv1.AckType_ACK_TYPE_PERSISTED)); err != nil {
		return fmt.Errorf("ack persisted command %q: %w", command.GetCommandId(), err)
	}
	return a.executeEntry(ctx, stream, entry)
}

func (a *Agent) executeEntry(ctx context.Context, stream Stream, entry *localstore.CommandEntry) error {
	var command operatorv1.Command
	if err := json.Unmarshal(entry.Payload, &command); err != nil {
		return a.finishFailure(ctx, stream, entry, Result{
			OperationID: entry.OperationID,
			CommandID:   entry.CommandID,
			Status:      "failed",
			Code:        "invalid_command",
			Message:     "invalid command payload",
		})
	}

	if err := a.store.UpdateStatus(ctx, entry.CommandID, localstore.StatusRunning, ""); err != nil {
		return fmt.Errorf("mark command %q running: %w", entry.CommandID, err)
	}

	result := a.execute(ctx, &command)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal command result %q: %w", entry.CommandID, err)
	}

	localStatus := localstore.StatusSucceeded
	if result.Status == "failed" {
		localStatus = localstore.StatusFailed
	}
	if err := a.store.UpdateStatus(ctx, entry.CommandID, localStatus, string(resultJSON)); err != nil {
		return fmt.Errorf("persist command result %q: %w", entry.CommandID, err)
	}

	if result.Status == "succeeded" && result.Release != nil && a.notifier != nil {
		a.notifier.NotifyOperationComplete(
			result.Release.Namespace,
			result.Release.Name,
			result.OperationID,
			result.DefinitionID,
		)
	}

	return stream.Send(resultRequest(&command, result, resultJSON))
}

func (a *Agent) execute(ctx context.Context, command *operatorv1.Command) Result {
	result := Result{
		OperationID:  command.GetOperationId(),
		CommandID:    command.GetCommandId(),
		Status:       "failed",
		DefinitionID: command.GetDefinitionId(),
	}

	if command.GetOperationType() != "INSTALL" {
		result.Code = "unsupported_command"
		result.Message = fmt.Sprintf("unsupported command type %q", command.GetOperationType())
		return result
	}
	if command.GetBundle() == nil || command.GetBundle().GetChartRef() == "" {
		result.Code = "invalid_command"
		result.Message = "chart_ref is required"
		return result
	}
	if command.GetNamespace() == "" || command.GetReleaseName() == "" {
		result.Code = "invalid_command"
		result.Message = "namespace and release_name are required"
		return result
	}

	values := map[string]interface{}{}
	if len(command.GetValues()) > 0 {
		if err := json.Unmarshal(command.GetValues(), &values); err != nil {
			result.Code = "invalid_command"
			result.Message = "values must be canonical JSON"
			return result
		}
	}

	timeout := a.installFlags.Timeout
	if command.GetTimeoutSeconds() > 0 {
		timeout = time.Duration(command.GetTimeoutSeconds()) * time.Second
	}

	release, err := a.engine.Install(ctx, helmengine.InstallOptions{
		Namespace:       command.GetNamespace(),
		ReleaseName:     command.GetReleaseName(),
		ChartPath:       command.GetBundle().GetChartRef(),
		ChartVersion:    command.GetBundle().GetChartVersion(),
		Values:          values,
		Atomic:          a.installFlags.Atomic,
		CreateNamespace: command.GetCreateNamespace(),
		Timeout:         timeout,
	})
	if err != nil {
		result.Code = installErrorCode(err)
		result.Message = err.Error()
		return result
	}

	result.Status = "succeeded"
	result.Release = release
	result.InventorySync = true
	result.ResourceSummary.ManifestDigest = release.ManifestDigest
	return result
}

func (a *Agent) finishFailure(ctx context.Context, stream Stream, entry *localstore.CommandEntry, result Result) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal failure result: %w", err)
	}
	if err := a.store.UpdateStatus(ctx, entry.CommandID, localstore.StatusFailed, string(resultJSON)); err != nil {
		return fmt.Errorf("persist failure result: %w", err)
	}

	command := &operatorv1.Command{
		OutboxId:    entry.OutboxID,
		CommandId:   entry.CommandID,
		OperationId: entry.OperationID,
		Sequence:    entry.Sequence,
	}
	return stream.Send(resultRequest(command, result, resultJSON))
}

func (a *Agent) sendCachedResult(stream Stream, command *operatorv1.Command, resultJSON string) error {
	var result Result
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return fmt.Errorf("decode cached result for %q: %w", command.GetCommandId(), err)
	}
	return stream.Send(resultRequest(command, result, []byte(resultJSON)))
}

func ackRequest(command *operatorv1.Command, ackType operatorv1.AckType) *operatorv1.CommandStreamRequest {
	return &operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Ack{
			Ack: &operatorv1.Ack{
				OutboxId:  command.GetOutboxId(),
				CommandId: command.GetCommandId(),
				Sequence:  command.GetSequence(),
				AckType:   ackType,
			},
		},
	}
}

func resultRequest(command *operatorv1.Command, result Result, resultJSON []byte) *operatorv1.CommandStreamRequest {
	return &operatorv1.CommandStreamRequest{
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
	}
}

func installErrorCode(err error) string {
	switch {
	case errors.Is(err, helmengine.ErrAlreadyExists):
		return "release_already_exists"
	case errors.Is(err, helmengine.ErrForbidden):
		return "forbidden"
	case errors.Is(err, helmengine.ErrTimeout):
		return "timeout"
	case errors.Is(err, helmengine.ErrCancelled):
		return "cancelled"
	default:
		return "helm_install_failed"
	}
}

// ConnectClient adapts the generated Connect client to StreamClient.
type ConnectClient struct {
	Client operatorv1connect.OperatorServiceClient
}

// CommandStream opens a generated Connect bidirectional stream.
func (c ConnectClient) CommandStream(ctx context.Context) Stream {
	return c.Client.CommandStream(ctx)
}
