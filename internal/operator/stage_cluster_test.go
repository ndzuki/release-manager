package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/preflight"
)

// recordingNamespaceEnsurer records the namespaces it was asked to create.
type recordingNamespaceEnsurer struct {
	created []string
	err     error
}

func (e *recordingNamespaceEnsurer) EnsureNamespace(_ context.Context, name string) error {
	e.created = append(e.created, name)
	return e.err
}

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
	executor := NewClusterStageExecutor(&fakeChartLocator{}, runner, nil, false, nil)

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
	executor := NewClusterStageExecutor(&fakeChartLocator{}, runner, nil, false, nil)

	_, err := executor.ExecuteStage(context.Background(), clusterStageCommand())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rejected 2 of 2")
}

// A rejected dry-run must name WHICH object the cluster refused and WHY. A bare
// count left the operator log and the stage result without a root cause, so the
// cause had to be recovered from a fresh environment run with extra logging
// (the e2e UPGRADE cluster-stage failure).
//
// The diagnostic is built from preflight.ResourceResult only — GVK, name,
// namespace, stable error code and the API server's already-sanitized reason —
// so no object body or Secret value can reach the stage error.
func TestClusterStageFailureNamesTheRejectedObject(t *testing.T) {
	runner := &fakeDryRunner{batch: &preflight.BatchResult{
		Passed: false, ResourceCount: 1,
		Results: []preflight.ResourceResult{{
			GVK:       schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			Name:      "release-fixture",
			Namespace: "e2e-release",
			Rejected:  true,
			ErrorCode: preflight.ErrUnknown,
			Reason:    `deployments.apps "release-fixture" already exists`,
		}},
	}}
	executor := NewClusterStageExecutor(&fakeChartLocator{}, runner, nil, false, nil)

	_, err := executor.ExecuteStage(context.Background(), clusterStageCommand())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rejected 1 of 1")
	assert.Contains(t, err.Error(), "apps/v1/Deployment release-fixture")
	assert.Contains(t, err.Error(), "namespace e2e-release")
	assert.Contains(t, err.Error(), "[preflight_unknown]")
	assert.Contains(t, err.Error(), "already exists")
}

// A batch that carries no rejected detail must still render a readable
// diagnostic instead of an empty suffix.
func TestDescribeRejectedFallbacks(t *testing.T) {
	assert.Equal(t, "no rejected resource recorded", describeRejected(&preflight.BatchResult{}))
	assert.Equal(t, "unknown resource", describeRejected(&preflight.BatchResult{
		Results: []preflight.ResourceResult{{Rejected: true}},
	}))
	assert.Equal(t, "v1/ConfigMap cm-1", describeRejected(&preflight.BatchResult{
		Results: []preflight.ResourceResult{{
			GVK:      schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"},
			Name:     "cm-1",
			Rejected: true,
		}},
	}))
}

// A dry-runner failure surfaces as a stage failure, not a silent pass.
func TestClusterStagePropagatesADryRunFailure(t *testing.T) {
	runner := &fakeDryRunner{err: assert.AnError}
	executor := NewClusterStageExecutor(&fakeChartLocator{}, runner, nil, false, nil)

	_, err := executor.ExecuteStage(context.Background(), clusterStageCommand())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster stage")
}

func TestClusterStageRejectsIncompleteCommands(t *testing.T) {
	executor := NewClusterStageExecutor(&fakeChartLocator{}, &fakeDryRunner{}, nil, false, nil)

	noBundle := clusterStageCommand()
	noBundle.Bundle = nil
	_, err := executor.ExecuteStage(context.Background(), noBundle)
	assert.Error(t, err)

	_, err = executor.ExecuteStage(context.Background(), nil)
	assert.Error(t, err)

	noRunner := NewClusterStageExecutor(&fakeChartLocator{}, nil, nil, false, nil)
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

// TASK-114: an INSTALL creates its target namespace, so the cluster stage must
// create it before dry-running. Without this a first INSTALL into a fresh
// namespace is rejected as namespace_missing and can never pass its own
// preflight (real CI run 2026-09-21: the dev fixture's e2e-release namespace
// does not pre-exist in the customer clusters).
func TestClusterStageEnsuresTheNamespaceWhenTheCommandCreatesIt(t *testing.T) {
	runner := &fakeDryRunner{batch: &preflight.BatchResult{Passed: true, ResourceCount: 1}}
	ensurer := &recordingNamespaceEnsurer{}
	executor := NewClusterStageExecutor(&fakeChartLocator{}, runner, ensurer, false, nil)

	command := clusterStageCommand()
	command.CreateNamespace = true
	_, err := executor.ExecuteStage(context.Background(), command)
	require.NoError(t, err)

	assert.Equal(t, []string{command.GetNamespace()}, ensurer.created,
		"the stage must create the target namespace before dry-running into it")
	assert.Equal(t, 1, runner.objects, "the dry-run still runs after the namespace exists")
}

// A command that does not create its namespace must not create one: a missing
// namespace stays namespace_missing (REQ-047).
func TestClusterStageLeavesTheNamespaceAloneOtherwise(t *testing.T) {
	runner := &fakeDryRunner{batch: &preflight.BatchResult{Passed: true, ResourceCount: 1}}
	ensurer := &recordingNamespaceEnsurer{}
	executor := NewClusterStageExecutor(&fakeChartLocator{}, runner, ensurer, false, nil)

	command := clusterStageCommand()
	command.CreateNamespace = false
	_, err := executor.ExecuteStage(context.Background(), command)
	require.NoError(t, err)

	assert.Empty(t, ensurer.created, "a command that does not create its namespace must not create one")
}

// A command that creates its namespace fails closed when no ensurer is wired,
// rather than dry-running into a namespace that may not exist.
func TestClusterStageRequiresANamespaceEnsurerWhenTheCommandCreatesIt(t *testing.T) {
	runner := &fakeDryRunner{batch: &preflight.BatchResult{Passed: true, ResourceCount: 1}}
	executor := NewClusterStageExecutor(&fakeChartLocator{}, runner, nil, false, nil)

	command := clusterStageCommand()
	command.CreateNamespace = true
	_, err := executor.ExecuteStage(context.Background(), command)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace ensurer")
	assert.Zero(t, runner.objects, "the dry-run must not run without the namespace")
}

// KubeNamespaceEnsurer is idempotent: AlreadyExists is success, an empty name is
// not.
func TestKubeNamespaceEnsurerIsIdempotent(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	ensurer := NewKubeNamespaceEnsurer(client)

	require.NoError(t, ensurer.EnsureNamespace(context.Background(), "e2e-release"))
	require.NoError(t, ensurer.EnsureNamespace(context.Background(), "e2e-release"),
		"a retry must treat AlreadyExists as success")

	_, err := client.CoreV1().Namespaces().Get(context.Background(), "e2e-release", metav1.GetOptions{})
	require.NoError(t, err)

	require.Error(t, ensurer.EnsureNamespace(context.Background(), "  "))
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
