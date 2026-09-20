package operator

import (
	"context"
	"fmt"
	"strings"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
)

// StageExecutor runs one preflight stage's validation for a delivered command.
//
// A stage is a check, not a release write: an executor must never install or
// upgrade, and it reports its own result.
type StageExecutor interface {
	ExecuteStage(ctx context.Context, command *operatorv1.Command) (string, error)
}

// StageDispatcher routes a command carrying a preflight stage to that stage's
// executor, and every other command to the ordinary execution path.
//
// The routing is the fix for TASK-114: the orchestrator has always dispatched
// stage commands, but nothing on the operator side recognised the stage, so a
// stage fell through to the operation-type branch and performed a real install.
// Two rules keep that from coming back:
//
//   - a command with no stage keeps exactly the behaviour it had before;
//   - a command with an *unknown* stage fails closed rather than falling back,
//     because falling back is the release write this type exists to prevent.
type StageDispatcher struct {
	inner  CommandExecutor
	stages map[string]StageExecutor
}

// NewStageDispatcher builds the dispatcher. inner may be nil only when every
// delivered command carries a stage; otherwise Execute fails closed.
func NewStageDispatcher(inner CommandExecutor, stages map[string]StageExecutor) *StageDispatcher {
	copied := make(map[string]StageExecutor, len(stages))
	for stage, executor := range stages {
		copied[stage] = executor
	}
	return &StageDispatcher{inner: inner, stages: copied}
}

// Execute routes by stage. A stage command never reaches inner, whatever the
// stage is: unknown stages fail closed instead.
func (d *StageDispatcher) Execute(ctx context.Context, command *operatorv1.Command) (string, error) {
	if command == nil {
		return "", fmt.Errorf("operator command is required")
	}
	stage := strings.TrimSpace(command.GetStage())
	if stage == "" {
		if d.inner == nil {
			return "", fmt.Errorf("operator command executor is required")
		}
		return d.inner.Execute(ctx, command)
	}
	executor, ok := d.stages[stage]
	if !ok || executor == nil {
		// Fail closed: executing this as an ordinary command would install or
		// upgrade the release while the caller believes a check ran.
		return "", fmt.Errorf("unsupported preflight stage %q: refusing to execute it as a release write", stage)
	}
	return executor.ExecuteStage(ctx, command)
}

var _ CommandExecutor = (*StageDispatcher)(nil)
