package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
	"github.com/ndzuki/release-manager/internal/operator/preflight"
)

type CommandExecutor interface {
	Execute(context.Context, *operatorv1.Command) (string, error)
}

type RuntimeCommandExecutor struct {
	store  localstore.Store
	pull   *preflight.RuntimePullExecutor
	logger *slog.Logger
}

func NewRuntimeCommandExecutor(
	st localstore.Store,
	pull *preflight.RuntimePullExecutor,
	logger *slog.Logger,
) *RuntimeCommandExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &RuntimeCommandExecutor{store: st, pull: pull, logger: logger}
}

func (e *RuntimeCommandExecutor) Execute(ctx context.Context, command *operatorv1.Command) (string, error) {
	if e == nil || e.store == nil {
		return "", fmt.Errorf("operator command store is required")
	}
	if command == nil || strings.TrimSpace(command.GetCommandId()) == "" {
		return "", fmt.Errorf("operator command is required")
	}

	payload, err := json.Marshal(command)
	if err != nil {
		return "", fmt.Errorf("marshal operator command: %w", err)
	}
	entry := &localstore.CommandEntry{
		CommandID:   command.GetCommandId(),
		OutboxID:    command.GetOutboxId(),
		OperationID: command.GetOperationId(),
		Sequence:    command.GetSequence(),
		Payload:     payload,
		Status:      localstore.StatusRunning,
	}
	if err := e.store.Save(ctx, entry); err != nil {
		return "", fmt.Errorf("persist operator command: %w", err)
	}

	images := make([]string, 0)
	if bundle := command.GetBundle(); bundle != nil {
		images = make([]string, 0, len(bundle.GetImages()))
		for _, image := range bundle.GetImages() {
			ref := strings.TrimSpace(image.GetRef())
			digest := strings.TrimSpace(image.GetDigest())
			if ref == "" || digest == "" {
				return e.fail(ctx, command, entry, fmt.Errorf("image ref and digest are required"))
			}
			if !strings.Contains(ref, "@") {
				ref += "@" + digest
			}
			images = append(images, ref)
		}
	}

	result := map[string]any{
		"operation_id": command.GetOperationId(),
		"command_id":   command.GetCommandId(),
		"runtime_pull": map[string]any{
			"enabled": false,
			"passed":  true,
		},
	}
	if e.pull != nil && e.pull.Enabled() {
		pullResult, pullErr := e.pull.Run(ctx, command.GetOperationId(), images)
		if pullErr != nil {
			return e.fail(ctx, command, entry, fmt.Errorf("runtime pull preflight: %w", pullErr))
		}
		result["runtime_pull"] = pullResult
		if !e.pull.AllowsExecution(pullResult) {
			return e.fail(ctx, command, entry, fmt.Errorf("runtime pull preflight failed"))
		}
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return e.fail(ctx, command, entry, fmt.Errorf("marshal operator command result: %w", err))
	}
	resultString := string(resultJSON)
	if err := e.store.UpdateStatus(ctx, command.GetCommandId(), localstore.StatusSucceeded, resultString); err != nil {
		return "", fmt.Errorf("persist operator command result: %w", err)
	}
	return resultString, nil
}

func (e *RuntimeCommandExecutor) fail(
	ctx context.Context,
	command *operatorv1.Command,
	entry *localstore.CommandEntry,
	execErr error,
) (string, error) {
	message := execErr.Error()
	if err := e.store.UpdateStatus(ctx, entry.CommandID, localstore.StatusFailed, message); err != nil {
		return "", fmt.Errorf("persist failed operator command: %w", err)
	}
	e.logger.Warn("operator command execution failed", "command_id", command.GetCommandId(), "error", execErr)
	return "", execErr
}

var _ CommandExecutor = (*RuntimeCommandExecutor)(nil)
