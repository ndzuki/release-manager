package agent

import (
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/observer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncTestStream is a thread-safe test stream for concurrent reporter sends.
type syncTestStream struct {
	mu   sync.Mutex
	sent []*operatorv1.CommandStreamRequest
}

func newSyncTestStream() *syncTestStream { return &syncTestStream{} }

func (s *syncTestStream) Send(req *operatorv1.CommandStreamRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, req)
	return nil
}

func (*syncTestStream) Receive() (*operatorv1.CommandStreamResponse, error) {
	return nil, errors.New("not implemented")
}
func (*syncTestStream) CloseRequest() error  { return nil }
func (*syncTestStream) CloseResponse() error { return nil }

func (s *syncTestStream) all() []*operatorv1.CommandStreamRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*operatorv1.CommandStreamRequest(nil), s.sent...)
}

func (s *syncTestStream) progress() []*operatorv1.RolloutProgress {
	out := make([]*operatorv1.RolloutProgress, 0, 2)
	for _, req := range s.all() {
		if p := req.GetRolloutProgress(); p != nil {
			out = append(out, p)
		}
	}
	return out
}

func (s *syncTestStream) waitForProgress(t *testing.T, count int) []*operatorv1.RolloutProgress {
	t.Helper()
	var out []*operatorv1.RolloutProgress
	require.Eventually(t, func() bool {
		out = s.progress()
		return len(out) >= count
	}, 2*time.Second, 5*time.Millisecond)
	return out
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── REQ-077 AC-077-02: reporter throttle, change detection, terminal flush ──

func TestRolloutReporter_ChangeDetectionAndThrottle(t *testing.T) {
	stream := newSyncTestStream()
	reporter := newRolloutReporter(stream, "op-1", discardLogger())
	reporter.throttle = 20 * time.Millisecond

	ref := "deployments/app/default"
	// First change: reported.
	reporter.report(ref, 0, 3, false)
	// Unchanged counters: suppressed.
	reporter.report(ref, 0, 3, false)
	// Changed inside the throttle window: suppressed.
	reporter.report(ref, 1, 3, false)
	assert.Len(t, stream.progress(), 1, "only the first change is reported")

	time.Sleep(25 * time.Millisecond)
	// Changed after the window: reported.
	reporter.report(ref, 2, 3, false)
	// Force bypasses both gates (terminal flush).
	reporter.report(ref, 3, 3, true)

	progress := stream.progress()
	require.Len(t, progress, 3)
	assert.Equal(t, "op-1", progress[0].GetOperationId())
	assert.Equal(t, ref, progress[0].GetWorkloadRef())
	assert.Equal(t, int32(0), progress[0].GetReady())
	assert.Equal(t, int32(2), progress[1].GetReady())
	assert.Equal(t, int32(3), progress[1].GetDesired())
	assert.Equal(t, int32(3), progress[2].GetReady())
}

func TestRolloutReporter_TracksIndependentWorkloads(t *testing.T) {
	stream := newSyncTestStream()
	reporter := newRolloutReporter(stream, "op-1", discardLogger())
	reporter.throttle = 10 * time.Second

	reporter.report("deployments/app/web", 1, 3, false)
	reporter.report("jobs/app/migrate", 0, 0, false)

	progress := stream.progress()
	require.Len(t, progress, 2)
	assert.Equal(t, "deployments/app/web", progress[0].GetWorkloadRef())
	assert.Equal(t, "jobs/app/migrate", progress[1].GetWorkloadRef())
}

// ── REQ-077 AC-077-02: agent wiring (install path) ──

func TestAgent_InstallReportsRolloutProgress(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{
			Name: "example", Namespace: "apps", Revision: 1, Status: "deployed",
			ManifestDigest: "sha256:manifest",
			Workloads: []helmengine.WorkloadSummary{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "apps", Name: "web"},
				{APIVersion: "batch/v1", Kind: "Job", Namespace: "apps", Name: "migrate"},
			},
		},
	}
	obs := observer.NewFake()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", Generation: 2}}
	obs.SetResponse(observer.ResourceRef{GVR: observer.DeploymentGVR, Namespace: "apps", Name: "web"},
		observer.FakeResponse{Behavior: observer.FakeImmediateReady, Result: observer.WatchResult{ReadyCount: 3, DesiredCount: 3}})
	obs.SetResponse(observer.ResourceRef{GVR: observer.JobGVR, Namespace: "apps", Name: "migrate"},
		observer.FakeResponse{Behavior: observer.FakeImmediateReady, Result: observer.WatchResult{ReadyCount: 0, DesiredCount: 0}})

	agent, err := New(Config{
		Client: noopClient{}, Engine: engine, Store: newMemoryStore(),
		SessionID: "session-1", OperatorID: "operator-1", Logger: discardLogger(),
		Observer: obs, KubeClient: fake.NewSimpleClientset(deployment),
		InstallFlags: InstallFlags{Atomic: true, Timeout: time.Minute},
	})
	require.NoError(t, err)

	stream := newSyncTestStream()
	require.NoError(t, agent.handleCommand(t.Context(), stream, installCommand("cmd-progress")))

	// 2 workloads × (initial report + unconditional terminal flush).
	progress := stream.waitForProgress(t, 4)
	refs := make(map[string]int)
	for _, p := range progress {
		assert.Equal(t, "op-1", p.GetOperationId())
		refs[p.GetWorkloadRef()]++
	}
	assert.Equal(t, 2, refs["deployments/apps/web"])
	assert.Equal(t, 2, refs["jobs/apps/migrate"])
	for _, p := range progress {
		if p.GetWorkloadRef() == "deployments/apps/web" {
			assert.Equal(t, int32(3), p.GetReady())
			assert.Equal(t, int32(3), p.GetDesired())
		} else {
			assert.Equal(t, int32(0), p.GetReady())
			assert.Equal(t, int32(0), p.GetDesired())
		}
	}

	// The command result is still delivered on the stream.
	var results int
	for _, req := range stream.all() {
		if req.GetResult() != nil {
			results++
		}
	}
	assert.Equal(t, 1, results)
}

func TestAgent_ProgressPrecedesResult(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{
			Name: "example", Namespace: "apps", Revision: 1, Status: "deployed",
			ManifestDigest: "sha256:manifest",
			Workloads: []helmengine.WorkloadSummary{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "apps", Name: "web"},
			},
		},
	}
	obs := observer.NewFake()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", Generation: 2}}
	// Observation holds open for 100ms before reporting ready.
	obs.SetResponse(observer.ResourceRef{GVR: observer.DeploymentGVR, Namespace: "apps", Name: "web"},
		observer.FakeResponse{Behavior: observer.FakeDelayedReady, Delay: 100 * time.Millisecond,
			Result: observer.WatchResult{ReadyCount: 1, DesiredCount: 1}})

	agent, err := New(Config{
		Client: noopClient{}, Engine: engine, Store: newMemoryStore(),
		SessionID: "session-1", OperatorID: "operator-1", Logger: discardLogger(),
		Observer: obs, KubeClient: fake.NewSimpleClientset(deployment),
		InstallFlags: InstallFlags{Atomic: true, Timeout: time.Minute},
	})
	require.NoError(t, err)

	stream := newSyncTestStream()
	started := time.Now()
	require.NoError(t, agent.handleCommand(t.Context(), stream, installCommand("cmd-delayed")))
	elapsed := time.Since(started)

	// Observation runs synchronously: the delayed observation must finish
	// before the result is sent, so handleCommand blocks until then.
	require.GreaterOrEqual(t, elapsed, 100*time.Millisecond, "result sent before observation finished (%s)", elapsed)

	// Progress (initial report + unconditional terminal flush) precedes the
	// result on the stream.
	reqs := stream.all()
	lastProgress, resultAt := -1, -1
	for i, req := range reqs {
		if req.GetRolloutProgress() != nil {
			lastProgress = i
		}
		if req.GetResult() != nil {
			resultAt = i
		}
	}
	require.NotEqual(t, -1, resultAt, "result missing from stream")
	require.Less(t, lastProgress, resultAt, "progress must precede the result (progress=%d result=%d)", lastProgress, resultAt)

	progress := stream.progress()
	require.Len(t, progress, 2) // initial + terminal flush
	assert.Equal(t, "deployments/apps/web", progress[0].GetWorkloadRef())
	assert.Equal(t, int32(1), progress[0].GetReady())
}

func TestAgent_ObservationTimeoutStillYieldsResult(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{
			Name: "example", Namespace: "apps", Revision: 1, Status: "deployed",
			ManifestDigest: "sha256:manifest",
			Workloads: []helmengine.WorkloadSummary{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "apps", Name: "web"},
			},
		},
	}
	obs := observer.NewFake()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", Generation: 2}}
	// Never becomes ready: the observation is bounded by the remaining budget.
	obs.SetResponse(observer.ResourceRef{GVR: observer.DeploymentGVR, Namespace: "apps", Name: "web"},
		observer.FakeResponse{Behavior: observer.FakeNeverReady, Result: observer.WatchResult{ReadyCount: 0, DesiredCount: 1}})

	agent, err := New(Config{
		Client: noopClient{}, Engine: engine, Store: newMemoryStore(),
		SessionID: "session-1", OperatorID: "operator-1", Logger: discardLogger(),
		Observer: obs, KubeClient: fake.NewSimpleClientset(deployment),
		InstallFlags: InstallFlags{Atomic: true, Timeout: time.Minute},
	})
	require.NoError(t, err)

	cmd := installCommand("cmd-never-ready")
	cmd.TimeoutSeconds = 1 // 1s remaining budget bounds the observation
	stream := newSyncTestStream()
	started := time.Now()
	require.NoError(t, agent.handleCommand(t.Context(), stream, cmd))
	elapsed := time.Since(started)

	// The observation timeout must not block the result: it yields after the
	// bounded budget, never forever.
	require.Less(t, elapsed, 5*time.Second, "observation timeout blocked the result (%s)", elapsed)
	for _, req := range stream.all() {
		if req.GetResult() != nil {
			return
		}
	}
	t.Fatal("result must still be delivered after the observation timeout")
}

func TestAgent_InstallWithoutObserverSkipsRolloutProgress(t *testing.T) {
	engine := &recordingEngine{
		release: &helmengine.Release{
			Name: "example", Namespace: "apps", Revision: 1, Status: "deployed",
			ManifestDigest: "sha256:manifest",
			Workloads: []helmengine.WorkloadSummary{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "apps", Name: "web"},
			},
		},
	}
	agent := newTestAgent(t, engine, newMemoryStore(), nil)
	stream := newSyncTestStream()

	require.NoError(t, agent.handleCommand(t.Context(), stream, installCommand("cmd-noobs")))

	require.Eventually(t, func() bool {
		for _, req := range stream.all() {
			if req.GetResult() != nil {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, stream.progress(), "no observer configured → no progress reports")
}
