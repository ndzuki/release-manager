package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorruntime "github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
)

// stubStageExecutor records the stage it was asked to run and returns a canned
// result, so a test can tell a dispatched stage from a fall-through.
type stubStageExecutor struct {
	calls int
	stage string
	out   string
	err   error
}

func (e *stubStageExecutor) ExecuteStage(_ context.Context, command *operatorv1.Command) (string, error) {
	e.calls++
	e.stage = command.GetStage()
	return e.out, e.err
}

func newStageAgent(t *testing.T, engine helmengine.Engine, stages map[string]operatorruntime.StageExecutor) *Agent {
	t.Helper()
	agent, err := New(Config{
		Client:     noopClient{},
		Engine:     engine,
		Store:      newMemoryStore(),
		SessionID:  "session-1",
		OperatorID: "operator-1",
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		InstallFlags: InstallFlags{
			Atomic:  true,
			Timeout: time.Minute,
		},
		Stages: operatorruntime.NewStageDispatcher(nil, stages),
	})
	require.NoError(t, err)
	return agent
}

// TASK-114 AC 2 + AC 5 ①: a stage command is a check, not a release write. It
// must reach its stage executor and must NOT touch the Helm engine — the
// command carries OperationType INSTALL and a full bundle, exactly the shape
// that used to run a real install four times per operation.
func TestAgent_StageCommandRunsTheStageAndNeverInstalls(t *testing.T) {
	for _, stage := range []string{"render", "cluster", "runtime_pull"} {
		t.Run(stage, func(t *testing.T) {
			engine := &recordingEngine{
				release: &helmengine.Release{Name: "example", Namespace: "apps", Revision: 1, Status: "deployed"},
			}
			executor := &stubStageExecutor{out: `{"stage":"` + stage + `","passed":true}`}
			agent := newStageAgent(t, engine, map[string]operatorruntime.StageExecutor{stage: executor})

			command := installCommand("cmd-" + stage)
			command.Stage = stage
			stream := newTestStream()

			require.NoError(t, agent.handleCommand(t.Context(), stream, command))

			assert.Equal(t, 1, executor.calls, "the stage command must reach its own executor")
			assert.Equal(t, stage, executor.stage)
			assert.Zero(t, engine.installCalls, "a stage command must never install the release")
			assert.Zero(t, engine.upgradeCalls, "a stage command must never upgrade the release")
			assert.Zero(t, engine.rollbackCalls, "a stage command must never roll back the release")

			require.Len(t, stream.sent, 2)
			result := stream.sent[1].GetResult()
			require.NotNil(t, result)
			assert.Equal(t, "succeeded", result.GetStatus())
			assert.Contains(t, result.GetResultJson(), `"detail":"{\"stage\":\"`+stage+`\"`)
		})
	}
}

// TASK-114 AC 2: an unknown stage fails closed. Falling back to the ordinary
// path would install the release while the caller believes a check ran.
func TestAgent_UnknownStageFailsClosedAndNeverInstalls(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{Name: "example", Namespace: "apps", Revision: 1, Status: "deployed"},
	}
	render := &stubStageExecutor{}
	agent := newStageAgent(t, engine, map[string]operatorruntime.StageExecutor{"render": render})

	command := installCommand("cmd-unknown")
	command.Stage = "not-a-stage"
	stream := newTestStream()

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))

	assert.Zero(t, render.calls, "an unknown stage must not reach any registered executor")
	assert.Zero(t, engine.installCalls, "an unknown stage must not fall back to install")

	require.Len(t, stream.sent, 2)
	result := stream.sent[1].GetResult()
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.GetStatus())
	assert.Contains(t, result.GetResultJson(), `"code":"preflight_stage_failed"`)
	assert.Contains(t, result.GetResultJson(), `"detail":"preflight_stage_failed: `)
}

// TASK-114 AC 2: without a dispatcher every stage command fails closed rather
// than executing as a release write.
func TestAgent_MissingStageDispatcherFailsClosed(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{Name: "example", Namespace: "apps", Revision: 1, Status: "deployed"},
	}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)

	command := installCommand("cmd-no-dispatcher")
	command.Stage = "render"
	stream := newTestStream()

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))

	assert.Zero(t, engine.installCalls, "a stage command must not install when the dispatcher is missing")
	require.Len(t, stream.sent, 2)
	result := stream.sent[1].GetResult()
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.GetStatus())
	assert.Contains(t, result.GetResultJson(), `"code":"preflight_stage_unavailable"`)
}

// TASK-114 AC 2: a stage executor's error is reported as a failed stage, and
// the error text stays in `message` while `detail` carries the stable code the
// orchestrator maps to a preflight error code.
func TestAgent_StageExecutorErrorIsReportedAsFailure(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{Name: "example", Namespace: "apps", Revision: 1, Status: "deployed"},
	}
	executor := &stubStageExecutor{err: errors.New("cluster stage: server-side dry-run rejected 1 of 1 objects")}
	agent := newStageAgent(t, engine, map[string]operatorruntime.StageExecutor{"cluster": executor})

	command := installCommand("cmd-fail")
	command.Stage = "cluster"
	stream := newTestStream()

	require.NoError(t, agent.handleCommand(t.Context(), stream, command))

	assert.Zero(t, engine.installCalls)
	require.Len(t, stream.sent, 2)
	result := stream.sent[1].GetResult()
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.GetStatus())
	assert.Contains(t, result.GetResultJson(), `"detail":"preflight_stage_failed: `,
		"the detail must lead with the stable code the orchestrator maps to a preflight error code")
	assert.Contains(t, result.GetResultJson(), "server-side dry-run rejected",
		"the detail must also keep the executor's message for diagnosis")
}

// TASK-114 AC 2: a command without a stage keeps the previous behaviour — it
// reaches the ordinary execution path and installs.
func TestAgent_CommandWithoutStageStillInstalls(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{Name: "example", Namespace: "apps", Revision: 1, Status: "deployed"},
	}
	render := &stubStageExecutor{}
	agent := newStageAgent(t, engine, map[string]operatorruntime.StageExecutor{"render": render})

	stream := newTestStream()
	require.NoError(t, agent.handleCommand(t.Context(), stream, installCommand("cmd-plain")))

	assert.Equal(t, 1, engine.installCalls, "an ordinary command keeps its execution path")
	assert.Zero(t, render.calls, "an ordinary command must not reach a stage executor")
	require.Len(t, stream.sent, 2)
	assert.Equal(t, "succeeded", stream.sent[1].GetResult().GetStatus())
}
