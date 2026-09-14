package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"
)

// failingStage returns one fixed error, so the assertion is about how the runner
// reports a stage failure rather than about the failure itself.
type failingStage struct {
	err error
}

func (s failingStage) Name() string { return "control-plane" }

func (s failingStage) Run(context.Context, *e2e.Fixture) error { return s.err }

func runFailingStage(t *testing.T, err error) e2e.StageResult {
	t.Helper()
	harness := e2e.New(e2e.Config{})
	report, runErr := harness.Run(context.Background(), e2e.Scenario{
		SelectedStages: []string{"control-plane"},
		Stages:         []e2e.StageSpec{{Name: "control-plane", Stage: failingStage{err: err}}},
		StageTimeout:   5 * time.Second,
	})
	require.NoError(t, runErr)
	require.Len(t, report.Results, 1)
	return report.Results[0]
}

// TestStageErrorCodeReachesTheArtifact locks the cross-package error-code
// contract. The stage package reports its code through ErrorCode() because
// StageError exposes Code as a field and a Go method cannot share that name;
// the runner must still surface the specific code rather than collapsing every
// live stage failure into the generic "stage_failed" (TASK-066).
func TestStageErrorCodeReachesTheArtifact(t *testing.T) {
	t.Parallel()

	result := runFailingStage(t, &stages.StageError{
		Code:      stages.CodeEnvironmentUnhealthy,
		Component: "control-plane",
		Detail:    "service is not ready",
	})

	assert.Equal(t, e2e.StageFail, result.Status)
	assert.Equal(t, stages.CodeEnvironmentUnhealthy, result.ErrorCode)
	// ErrorCode carries the machine code; RootCause carries the human message.
	assert.Contains(t, result.RootCause, "service is not ready")
	// The sentinel must stay reachable through the same error.
	assert.ErrorIs(t, result.Error, stages.ErrEnvironmentUnhealthy)
}

// TestStageErrorCodeSurvivesWrapping covers a stage that wraps its failure
// before returning it: a plain type assertion would miss the code entirely.
func TestStageErrorCodeSurvivesWrapping(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("stage body: %w", &stages.StageError{
		Code:      stages.CodeOperationRejected,
		Component: "release",
		Detail:    "server rejected the upgrade",
	})
	result := runFailingStage(t, wrapped)

	assert.Equal(t, stages.CodeOperationRejected, result.ErrorCode)
	assert.ErrorIs(t, result.Error, stages.ErrOperationRejected)
}

// TestUnclassifiedStageFailureStaysGeneric guards the other direction: an error
// with no code contract must not invent one.
func TestUnclassifiedStageFailureStaysGeneric(t *testing.T) {
	t.Parallel()

	result := runFailingStage(t, errors.New("transport exploded"))

	assert.Equal(t, "stage_failed", result.ErrorCode)
	assert.Equal(t, "transport exploded", result.RootCause)
}
