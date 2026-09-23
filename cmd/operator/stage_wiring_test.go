package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
)

// TASK-114 AC 3: cmd/operator assembles every preflight stage the coordinator
// dispatches. A partially assembled dispatcher would fail a stage closed and
// block every INSTALL, so this test asserts each stage reaches its own executor
// and that an unregistered stage still fails closed.
//
// The client construction performs no cluster I/O (clients and the REST mapper
// are lazy), so the test runs without a cluster.
func TestBuildStageDispatcherWiresEveryPreflightStage(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	restConfig := &rest.Config{Host: "https://127.0.0.1:1"}
	kubeClient, err := kubernetes.NewForConfig(restConfig)
	require.NoError(t, err)

	dispatcher, err := (&operatorSvc{}).buildStageDispatcher(
		helmengine.NewRealEngine("", logger), restConfig, kubeClient, logger)
	require.NoError(t, err)
	require.NotNil(t, dispatcher)

	// A command without a bundle makes each executor fail with a stage-specific
	// error. That is how the test tells routing from a fall-through to the
	// dispatcher's (nil) ordinary path.
	stageErrors := map[string]string{
		"artifact":     "artifact stage",
		"render":       "render stage",
		"cluster":      "cluster stage",
		"runtime_pull": "runtime pull stage",
	}
	for stage, want := range stageErrors {
		t.Run(stage, func(t *testing.T) {
			_, err := dispatcher.Execute(t.Context(), &operatorv1.Command{
				CommandId: "cmd-" + stage, OperationId: "op-1", Stage: stage,
			})
			require.Error(t, err, "the stage executor must report the missing input")
			assert.Contains(t, err.Error(), want, "the command must reach the %s executor", stage)
			assert.NotContains(t, err.Error(), "operator command executor is required",
				"a stage command must never fall through to the ordinary path")
		})
	}

	// TASK-114 AC 2: an unregistered stage fails closed instead of executing as
	// a release write.
	//
	// `artifact` used to be listed here: it was consumed by the orchestrator
	// locally and was deliberately not registered on the operator side. ADR-024
	// supersedes that design -- the artifact preflight must run on the operator
	// against the real chart archive -- so `artifact` is now registered (see
	// stageErrors above) and only a genuinely unknown stage reaches this loop.
	for _, stage := range []string{"not-a-stage"} {
		t.Run("unregistered-"+stage, func(t *testing.T) {
			_, err := dispatcher.Execute(t.Context(), &operatorv1.Command{
				CommandId: "cmd-" + stage, OperationId: "op-1", Stage: stage,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsupported preflight stage")
		})
	}
}

// TASK-114 AC 3: with no runtime_pull_preflight policy the operator builds a
// disabled executor. The stage is optional in ProductionStages(), so it fails
// closed — a check that did not run must not report success — without blocking
// the release.
func TestBuildStageDispatcherLeavesRuntimePullDisabledWithoutPolicy(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	restConfig := &rest.Config{Host: "https://127.0.0.1:1"}
	kubeClient, err := kubernetes.NewForConfig(restConfig)
	require.NoError(t, err)

	dispatcher, err := (&operatorSvc{}).buildStageDispatcher(
		helmengine.NewRealEngine("", logger), restConfig, kubeClient, logger)
	require.NoError(t, err)

	result, err := dispatcher.Execute(t.Context(), &operatorv1.Command{
		CommandId: "cmd-pull", OperationId: "op-1", Stage: "runtime_pull",
		Bundle: &commonv1.ReleaseBundle{
			ChartRef: "oci://registry.example.com/charts/example",
			Images: []*commonv1.BundleImage{{
				Ref: "localhost:5001/release-fixture:dev", Digest: "sha256:abc123",
				ValuesPath: "image.repository",
			}},
		},
	})
	// ADR-025 (Plan A) supersedes TASK-114 AC 2's "disabled is an error": the
	// stage reports skipped, which is what lets the control plane block on a
	// stage that RAN and failed while still allowing a cluster without the
	// capability. Reporting an error here would make the two indistinguishable.
	require.NoError(t, err, "a disabled runtime pull stage must not fail")
	assert.JSONEq(t, `{"status":"skipped","detail":"runtime_pull_disabled"}`, result)
}
