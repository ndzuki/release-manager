package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCanonicalHarnessFailsClosedWithoutImplementations locks the fail-closed
// contract: a canonical harness whose stage bodies are not wired must report a
// stage failure (ErrorCode not_implemented), never a vacuous pass. This is the
// pre-merge honesty gate that prevents `make e2e-all` from going green on
// stage implementations that do nothing (TASK-066).
func TestCanonicalHarnessFailsClosedWithoutImplementations(t *testing.T) {
	t.Parallel()

	for _, name := range CanonicalStages {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			harness := New(Config{})
			report, err := harness.Run(context.Background(), Scenario{
				SelectedStages: []string{name},
				StageTimeout:   time.Second,
			})
			require.NoError(t, err)
			require.Len(t, report.Results, 1)
			result := report.Results[0]
			assert.Equal(t, StageFail, result.Status)
			assert.Equal(t, "not_implemented", result.ErrorCode)
			assert.Equal(t, 1, report.Failed)
			assert.Equal(t, 1, report.ExitCode)

			// The stage error is recoverable through the sentinel.
			require.Error(t, result.Error)
			assert.ErrorIs(t, result.Error, ErrStageNotImplemented)
		})
	}
}

// TestCanonicalRunScenarioFailsClosedWithoutImplementations covers the
// RunScenario convenience entry point used by callers that do not supply a
// custom graph.
func TestCanonicalRunScenarioFailsClosedWithoutImplementations(t *testing.T) {
	t.Parallel()

	report, err := RunScenario(context.Background(), Scenario{
		SelectedStages: []string{StageRelease},
		StageTimeout:   time.Second,
	})
	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	assert.Equal(t, StageFail, report.Results[0].Status)
	assert.Equal(t, "not_implemented", report.Results[0].ErrorCode)
	assert.Equal(t, 1, report.ExitCode)
}

// TestUnimplementedStageErrorShape verifies the sentinel error serialization
// contract used by stage artifacts.
func TestUnimplementedStageErrorShape(t *testing.T) {
	t.Parallel()

	stage := UnimplementedStage(StageEmergency)
	assert.Equal(t, StageEmergency, stage.Name())

	err := stage.Run(context.Background(), NewFixture())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStageNotImplemented)
	assert.Equal(t, "stage emergency not implemented", err.Error())

	var notImpl *NotImplementedError
	require.ErrorAs(t, err, &notImpl)
	assert.Equal(t, StageEmergency, notImpl.Stage)
	assert.Equal(t, "not_implemented", notImpl.Code())
}

// TestNotImplementedStageAfterRecovery ensures an implementation can replace a
// fail-closed placeholder and the same scenario then passes — the fail-closed
// marker must not poison later runs (recovery-path coverage).
func TestNotImplementedStageAfterRecovery(t *testing.T) {
	t.Parallel()

	placeholder := UnimplementedSpec(StageControlPlane)
	report, err := NewHarness(placeholder).Run(context.Background(), Scenario{
		SelectedStages: []string{StageControlPlane},
		StageTimeout:   time.Second,
	})
	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	assert.Equal(t, StageFail, report.Results[0].Status)

	realSpec := StageSpec{
		Name: StageControlPlane,
		Stage: NewStage(StageControlPlane, func(context.Context, *Fixture) error {
			return nil
		}),
	}
	report, err = NewHarness(realSpec).Run(context.Background(), Scenario{
		SelectedStages: []string{StageControlPlane},
		StageTimeout:   time.Second,
	})
	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	assert.Equal(t, StagePass, report.Results[0].Status)
	assert.Equal(t, 0, report.ExitCode)
}
