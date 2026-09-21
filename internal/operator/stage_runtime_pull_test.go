package operator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/preflight"
)

func pullStageCommand() *operatorv1.Command {
	return &operatorv1.Command{
		CommandId: "cmd-1", OperationId: "op-1", Stage: "runtime_pull",
		Bundle: &commonv1.ReleaseBundle{
			ChartRef: "oci://reg/charts/app", ChartVersion: "1.0.0", ChartDigest: "sha256:chart",
			Images: []*commonv1.BundleImage{
				{Ref: "reg/app:v1", Digest: "sha256:aaa"},
				{Ref: "reg/app@sha256:bbb", Digest: "sha256:bbb"},
			},
		},
	}
}

// ADR-025 (Plan A), superseding TASK-114 AC 2: a stage that cannot run must not
// report a PASS, but it reports skipped rather than failing. The distinction is
// what lets the control plane block on a stage that ran and failed (REQ-048)
// while still allowing a cluster that has the capability disabled.
func TestRuntimePullStageReportsSkippedWhenPullIsDisabled(t *testing.T) {
	// A disabled executor with no prober: Run returns ErrPullDisabled.
	executor := NewRuntimePullStageExecutor(preflight.NewRuntimePullExecutor(nil, preflight.RuntimePullConfig{Enabled: false}), nil)

	result, err := executor.ExecuteStage(context.Background(), pullStageCommand())
	require.NoError(t, err, "disabled is not a failure (ADR-025)")

	var decoded struct {
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &decoded))
	// The control plane's StageStatus value; literal here because the
	// operator package must not import the orchestrator's preflight package.
	assert.Equal(t, "skipped", decoded.Status)
	assert.Equal(t, "runtime_pull_disabled", decoded.Detail)
}

// A nil pull executor is a configuration error, not a silent pass.
func TestRuntimePullStageRequiresAnExecutor(t *testing.T) {
	executor := NewRuntimePullStageExecutor(nil, nil)
	_, err := executor.ExecuteStage(context.Background(), pullStageCommand())
	assert.Error(t, err)

	_, err = executor.ExecuteStage(context.Background(), nil)
	assert.Error(t, err)
}

// The stage derives the same digest-pinned references the ordinary execution
// path pulls.
func TestStageImageRefsPinsEveryImageToItsDigest(t *testing.T) {
	images, err := stageImageRefs(pullStageCommand())
	require.NoError(t, err)
	assert.Equal(t, []string{"reg/app:v1@sha256:aaa", "reg/app@sha256:bbb"}, images,
		"an unpinned ref gains its digest; an already pinned ref is left alone")

	_, err = stageImageRefs(&operatorv1.Command{})
	assert.Error(t, err, "a command without a bundle cannot be pulled")

	incomplete := pullStageCommand()
	incomplete.Bundle.Images = []*commonv1.BundleImage{{Ref: "reg/app:v1"}}
	_, err = stageImageRefs(incomplete)
	assert.Error(t, err, "an image without a digest must not be pulled by tag alone")
}
