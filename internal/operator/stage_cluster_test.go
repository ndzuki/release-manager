package operator

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/preflight"
)

// fakeDryRunner records what it was asked to dry-run and returns a canned batch.
type fakeDryRunner struct {
	objects int
	input   preflight.Input
	batch   *preflight.BatchResult
	err     error
}

func (f *fakeDryRunner) DryRunAll(_ context.Context, resources []*unstructured.Unstructured, input preflight.Input) (*preflight.BatchResult, error) {
	f.objects = len(resources)
	f.input = input
	if f.err != nil {
		return nil, f.err
	}
	return f.batch, nil
}

func clusterStageCommand() *operatorv1.Command {
	cmd := renderStageCommand()
	cmd.Stage = "cluster"
	return cmd
}

// TASK-114 AC 2: the cluster stage renders for itself, decodes the rendered
// stream, and asks the cluster to dry-run every object.
func TestClusterStageDryRunsEveryRenderedObject(t *testing.T) {
	runner := &fakeDryRunner{batch: &preflight.BatchResult{Passed: true, ResourceCount: 1}}
	executor := NewClusterStageExecutor(&fakeChartLocator{}, runner, false, nil)

	result, err := executor.ExecuteStage(context.Background(), clusterStageCommand())
	require.NoError(t, err)

	assert.Equal(t, 1, runner.objects, "the rendered object must reach the dry-runner")
	assert.Equal(t, "op-1", runner.input.OperationID)
	assert.NotEmpty(t, runner.input.RenderDigest, "the batch is keyed by the render digest")
	assert.Contains(t, string(runner.input.ManifestStream), "kind: ConfigMap")
	assert.Contains(t, result, `"passed":true`)
}

// The stage fails when the cluster refuses an object: a dry-run that did not
// pass must not report success.
func TestClusterStageFailsWhenTheClusterRejects(t *testing.T) {
	runner := &fakeDryRunner{batch: &preflight.BatchResult{
		Passed: false, ResourceCount: 2,
		Results: []preflight.ResourceResult{{Rejected: true}, {Rejected: true}},
	}}
	executor := NewClusterStageExecutor(&fakeChartLocator{}, runner, false, nil)

	_, err := executor.ExecuteStage(context.Background(), clusterStageCommand())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rejected 2 of 2")
}

// A dry-runner failure surfaces as a stage failure, not a silent pass.
func TestClusterStagePropagatesADryRunFailure(t *testing.T) {
	runner := &fakeDryRunner{err: assert.AnError}
	executor := NewClusterStageExecutor(&fakeChartLocator{}, runner, false, nil)

	_, err := executor.ExecuteStage(context.Background(), clusterStageCommand())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster stage")
}

func TestClusterStageRejectsIncompleteCommands(t *testing.T) {
	executor := NewClusterStageExecutor(&fakeChartLocator{}, &fakeDryRunner{}, false, nil)

	noBundle := clusterStageCommand()
	noBundle.Bundle = nil
	_, err := executor.ExecuteStage(context.Background(), noBundle)
	assert.Error(t, err)

	_, err = executor.ExecuteStage(context.Background(), nil)
	assert.Error(t, err)

	noRunner := NewClusterStageExecutor(&fakeChartLocator{}, nil, false, nil)
	_, err = noRunner.ExecuteStage(context.Background(), clusterStageCommand())
	assert.Error(t, err)
}

// The manifest stream is joined deterministically and excludes NOTES.txt, so
// the same chart renders the same stream twice.
func TestManifestStreamIsStableAndSkipsNotes(t *testing.T) {
	rendered := map[string]string{
		"b/templates/svc.yaml":  "kind: Service\n",
		"a/templates/cm.yaml":   "kind: ConfigMap\n",
		"z/templates/NOTES.txt": "install notes",
	}
	stream := string(manifestStream(rendered))
	assert.NotContains(t, stream, "install notes")
	assert.Less(t, indexOf(stream, "ConfigMap"), indexOf(stream, "Service"),
		"the stream must be ordered by filename, not by map iteration")
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
