package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
)

type recordingExecutor struct {
	calls int
	stage string
	err   error
}

func (e *recordingExecutor) Execute(context.Context, *operatorv1.Command) (string, error) {
	e.calls++
	return "ordinary", e.err
}

func (e *recordingExecutor) ExecuteStage(_ context.Context, command *operatorv1.Command) (string, error) {
	e.calls++
	e.stage = command.GetStage()
	return "stage:" + command.GetStage(), e.err
}

func stageCommand(stage string) *operatorv1.Command {
	return &operatorv1.Command{CommandId: "cmd-1", OperationId: "op-1", Stage: stage}
}

// TASK-114 AC 2: a command without a stage keeps exactly the behaviour it had
// before the dispatcher existed.
func TestStageDispatcherPassesOrdinaryCommandsThrough(t *testing.T) {
	inner := &recordingExecutor{}
	render := &recordingExecutor{}
	dispatcher := NewStageDispatcher(inner, map[string]StageExecutor{"render": render})

	result, err := dispatcher.Execute(t.Context(), stageCommand(""))
	require.NoError(t, err)
	assert.Equal(t, "ordinary", result)
	assert.Equal(t, 1, inner.calls)
	assert.Zero(t, render.calls, "an ordinary command must not reach a stage executor")
}

// TASK-114 AC 2: every known stage reaches its own executor and never the
// ordinary path.
func TestStageDispatcherRoutesKnownStages(t *testing.T) {
	for _, stage := range []string{"render", "cluster", "runtime_pull"} {
		t.Run(stage, func(t *testing.T) {
			inner := &recordingExecutor{}
			executor := &recordingExecutor{}
			dispatcher := NewStageDispatcher(inner, map[string]StageExecutor{stage: executor})

			result, err := dispatcher.Execute(t.Context(), stageCommand(stage))
			require.NoError(t, err)
			assert.Equal(t, "stage:"+stage, result)
			assert.Equal(t, 1, executor.calls)
			assert.Equal(t, stage, executor.stage)
			assert.Zero(t, inner.calls, "a stage command must never take the ordinary path")
		})
	}
}

// TASK-114 AC 2, the invariant that matters: an unknown stage fails closed.
// Falling back to the ordinary path would install or upgrade the release while
// the caller believes a check ran -- the "false pass" this dispatcher exists to
// prevent.
func TestStageDispatcherFailsClosedOnAnUnknownStage(t *testing.T) {
	inner := &recordingExecutor{}
	dispatcher := NewStageDispatcher(inner, map[string]StageExecutor{"render": &recordingExecutor{}})

	_, err := dispatcher.Execute(t.Context(), stageCommand("not_a_stage"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported preflight stage")
	assert.Zero(t, inner.calls, "an unknown stage must not fall back to the ordinary path")
}

// A command with a stage but no executor registered is the same refusal.
func TestStageDispatcherFailsClosedWithoutAnExecutor(t *testing.T) {
	inner := &recordingExecutor{}
	dispatcher := NewStageDispatcher(inner, nil)

	_, err := dispatcher.Execute(t.Context(), stageCommand("render"))
	require.Error(t, err)
	assert.Zero(t, inner.calls)
}

// A nil command is rejected rather than panicking.
func TestStageDispatcherRejectsANilCommand(t *testing.T) {
	dispatcher := NewStageDispatcher(&recordingExecutor{}, nil)
	_, err := dispatcher.Execute(t.Context(), nil)
	assert.Error(t, err)
}
