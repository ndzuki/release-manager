package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/observer"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// rolloutProgressThrottle is the per-operation window between progress sends
// (REQ-077: 10s throttle; only counter changes within a window are sent).
const rolloutProgressThrottle = 10 * time.Second

// rolloutCounts is the ready/desired pair reported for one workload.
type rolloutCounts struct {
	ready   int32
	desired int32
}

// rolloutReporter serializes throttled rollout_progress sends for one
// operation onto the command stream. State converges in this single point
// even when multiple workloads are observed concurrently (REQ-077 Q5).
type rolloutReporter struct {
	stream      Stream
	operationID string
	logger      *slog.Logger
	throttle    time.Duration

	mu        sync.Mutex
	lastSent  map[string]time.Time
	lastState map[string]rolloutCounts
}

// newRolloutReporter builds a reporter bound to one operation's stream.
func newRolloutReporter(stream Stream, operationID string, logger *slog.Logger) *rolloutReporter {
	return &rolloutReporter{
		stream:      stream,
		operationID: operationID,
		logger:      logger,
		throttle:    rolloutProgressThrottle,
		lastSent:    make(map[string]time.Time),
		lastState:   make(map[string]rolloutCounts),
	}
}

// report sends a rollout_progress update when the counters changed and the
// throttle window allows it; force bypasses both for the unconditional
// terminal flush (AC-077-02: ready/failed/timeout always emits a final
// update). Send failures are best-effort per ADR-005 and only logged.
func (r *rolloutReporter) report(workloadRef string, ready, desired int32, force bool) {
	counts := rolloutCounts{ready: ready, desired: desired}
	r.mu.Lock()
	defer r.mu.Unlock()

	if !force {
		if last, ok := r.lastState[workloadRef]; ok && last == counts {
			return
		}
		if sent, ok := r.lastSent[workloadRef]; ok && time.Since(sent) < r.throttle {
			return
		}
	}
	r.lastState[workloadRef] = counts
	r.lastSent[workloadRef] = time.Now()

	if err := r.stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_RolloutProgress{
			RolloutProgress: &operatorv1.RolloutProgress{
				OperationId: r.operationID,
				WorkloadRef: workloadRef,
				Ready:       ready,
				Desired:     desired,
			},
		},
	}); err != nil && r.logger != nil {
		r.logger.Debug("rollout progress send failed", "operation_id", r.operationID, "workload_ref", workloadRef, "error", err)
	}
}

// observeRollout watches the four-GVR workloads of a successful standard
// operation (INSTALL/UPGRADE/ROLLBACK) and reports ready/desired counters over
// the command stream (AC-077-02). Observation runs synchronously to a bounded
// deadline — the command's remaining time budget — so every progress report,
// including the unconditional terminal flush, reaches the orchestrator BEFORE
// the operation Result terminalizes the command (AC-077-17 then drops any
// late report). Observation failure or timeout never changes the operation
// outcome (REQ-077 Q2/Q5: enhancement, not a terminal-state precondition).
func (a *Agent) observeRollout(
	ctx context.Context,
	operationID string,
	workloads []helmengine.WorkloadSummary,
	timeout time.Duration,
	reporter *rolloutReporter,
) {
	if a.observer == nil || len(workloads) == 0 || timeout <= 0 {
		return
	}
	var wg sync.WaitGroup
	for _, workload := range workloads {
		workload := workload
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref := observerResourceRef(workload)
			if ref.GVR.Empty() {
				a.logger.Debug("skipping unsupported rollout workload", "kind", workload.Kind, "api_version", workload.APIVersion)
				return
			}
			// REQ-077 Q5: expectedGeneration comes from the workload's
			// current generation at observation start, not from Helm.
			expectedGeneration := a.currentGeneration(ctx, workload)
			result, err := a.observer.ObserveWithProgress(
				ctx,
				ref,
				expectedGeneration,
				timeout,
				func(r observer.WatchResult) {
					reporter.report(workloadRef(ref), r.ReadyCount, r.DesiredCount, false)
				},
			)
			// Unconditional terminal flush (AC-077-02: ready/failed/timeout).
			reporter.report(workloadRef(ref), result.ReadyCount, result.DesiredCount, true)
			if err != nil && !errors.Is(err, observer.ErrRolloutTimeout) {
				a.logger.Debug("rollout observation failed",
					"operation_id", operationID, "workload_ref", workloadRef(ref), "error", err)
			}
		}()
	}
	wg.Wait()
}

// observerResourceRef maps a manifest workload identity to the observer's
// typed resource ref; empty GVR means the kind is outside the four-GVR
// whitelist.
func observerResourceRef(workload helmengine.WorkloadSummary) observer.ResourceRef {
	ref := observer.ResourceRef{Namespace: workload.Namespace, Name: workload.Name}
	switch {
	case workload.APIVersion == "apps/v1" && workload.Kind == "Deployment":
		ref.GVR = observer.DeploymentGVR
	case workload.APIVersion == "apps/v1" && workload.Kind == "StatefulSet":
		ref.GVR = observer.StatefulSetGVR
	case workload.APIVersion == "apps/v1" && workload.Kind == "DaemonSet":
		ref.GVR = observer.DaemonSetGVR
	case workload.APIVersion == "batch/v1" && workload.Kind == "Job":
		ref.GVR = observer.JobGVR
	}
	return ref
}

// workloadRef renders the wire-format workload reference
// "<gvr.resource>/<namespace>/<name>" (REQ-077, e.g. deployments/app/default).
func workloadRef(ref observer.ResourceRef) string {
	return ref.GVR.Resource + "/" + ref.Namespace + "/" + ref.Name
}

// currentGeneration resolves the workload's current metadata generation from
// the cluster (REQ-077 Q5; REQ-024 generation gate). Jobs have no generation
// gate and return 0. Failures degrade to 0, which the observer rejects for
// apps workloads — observation is best-effort and must not block results.
func (a *Agent) currentGeneration(ctx context.Context, workload helmengine.WorkloadSummary) int64 {
	if a.kubeClient == nil {
		return 0
	}
	switch {
	case workload.APIVersion == "apps/v1" && workload.Kind == "Deployment":
		obj, err := a.kubeClient.AppsV1().Deployments(workload.Namespace).Get(ctx, workload.Name, metav1.GetOptions{})
		if err == nil {
			return obj.Generation
		}
	case workload.APIVersion == "apps/v1" && workload.Kind == "StatefulSet":
		obj, err := a.kubeClient.AppsV1().StatefulSets(workload.Namespace).Get(ctx, workload.Name, metav1.GetOptions{})
		if err == nil {
			return obj.Generation
		}
	case workload.APIVersion == "apps/v1" && workload.Kind == "DaemonSet":
		obj, err := a.kubeClient.AppsV1().DaemonSets(workload.Namespace).Get(ctx, workload.Name, metav1.GetOptions{})
		if err == nil {
			return obj.Generation
		}
	case workload.APIVersion == "batch/v1" && workload.Kind == "Job":
		return 0
	}
	return 0
}
