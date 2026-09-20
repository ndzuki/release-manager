package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/preflight"
)

// RuntimePullStageExecutor runs the preflight runtime_pull stage: it pulls the
// bundle's images into the node-local cache and reports the batch result.
//
// A stage that cannot run must fail, not pass: the ordinary execution path
// treats a disabled pull as "nothing to check" because it is a gate on the way
// to a release write, but a runtime_pull *stage* exists to check, so a disabled
// executor is an error here (TASK-114 AC 2 -- a stage is a check, and a check
// that did not happen must not report success).
type RuntimePullStageExecutor struct {
	pull   *preflight.RuntimePullExecutor
	logger *slog.Logger
}

// NewRuntimePullStageExecutor builds the runtime pull stage executor.
func NewRuntimePullStageExecutor(pull *preflight.RuntimePullExecutor, logger *slog.Logger) *RuntimePullStageExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &RuntimePullStageExecutor{pull: pull, logger: logger}
}

// ExecuteStage pulls the bundle's images and reports the batch result.
func (e *RuntimePullStageExecutor) ExecuteStage(ctx context.Context, command *operatorv1.Command) (string, error) {
	if e == nil || e.pull == nil {
		return "", fmt.Errorf("runtime pull stage requires a pull executor")
	}
	if command == nil {
		return "", fmt.Errorf("runtime pull stage requires a command")
	}
	images, err := stageImageRefs(command)
	if err != nil {
		return "", fmt.Errorf("runtime pull stage: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	result, err := e.pull.Run(ctx, command.GetOperationId(), images)
	if err != nil {
		// Includes ErrPullDisabled: a stage that cannot run fails closed rather
		// than reporting a pass it did not earn.
		return "", fmt.Errorf("runtime pull stage: %w", err)
	}
	if !e.pull.AllowsExecution(result) {
		return "", fmt.Errorf("runtime pull stage: runtime pull preflight failed")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("runtime pull stage: encode result: %w", err)
	}
	e.logger.Info("preflight runtime pull stage passed",
		"operation_id", command.GetOperationId(), "images", len(images))
	return string(encoded), nil
}

// stageImageRefs derives the digest-pinned image references a stage must pull.
// The same derivation the ordinary execution path uses, so a stage and an
// execution pull the identical references.
func stageImageRefs(command *operatorv1.Command) ([]string, error) {
	bundle := command.GetBundle()
	if bundle == nil {
		return nil, fmt.Errorf("bundle is required")
	}
	images := make([]string, 0, len(bundle.GetImages()))
	for _, image := range bundle.GetImages() {
		ref := strings.TrimSpace(image.GetRef())
		digest := strings.TrimSpace(image.GetDigest())
		if ref == "" || digest == "" {
			return nil, fmt.Errorf("image ref and digest are required")
		}
		if !strings.Contains(ref, "@") {
			ref += "@" + digest
		}
		images = append(images, ref)
	}
	return images, nil
}

var _ StageExecutor = (*RuntimePullStageExecutor)(nil)
