package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/vulnerability"
)

// fakeEvaluator records calls and answers with a scripted result.
type fakeEvaluator struct {
	mu     sync.Mutex
	calls  int
	result *vulnerability.AdmissionResult
	err    error
}

func (f *fakeEvaluator) Evaluate(context.Context, string, string) (*vulnerability.AdmissionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.result, f.err
}

func (f *fakeEvaluator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// recordingSink captures the audit events the admission step emits.
type recordingSink struct {
	mu     sync.Mutex
	events []*store.AuditEvent
}

func (s *recordingSink) Emit(ev *store.AuditEvent) audit.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return audit.Result{Accepted: true}
}

func (s *recordingSink) all() []*store.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*store.AuditEvent(nil), s.events...)
}

func admissionService(mode AdmissionMode, eval artifactEvaluator, sink *recordingSink) *Service {
	svc := &Service{
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		admissionMode:     mode,
		admissionCounters: &AdmissionMetrics{},
		vulnEval:          eval,
	}
	if sink != nil {
		svc.auditEmitter = sink
	}
	return svc
}

// TestApplyAdmissionModeMatrix pins the whole mode x outcome matrix. pass and warn
// are allowed in every mode (warn is the policy saying "acceptable but notable"),
// and the default mode is shadow, which can never block.
func TestApplyAdmissionModeMatrix(t *testing.T) {
	cases := []struct {
		mode         AdmissionMode
		outcome      admissionOutcome
		wantAllow    bool
		wantReason   string
		wantEvidence string
	}{
		{AdmissionOff, outcomePass, true, "", ""},
		{AdmissionOff, outcomeWarn, true, "", ""},
		{AdmissionOff, outcomeReject, true, "", ""},
		{AdmissionOff, outcomeUnavailable, true, "", ""},

		{AdmissionShadow, outcomePass, true, "", ""},
		{AdmissionShadow, outcomeWarn, true, "", ""},
		{AdmissionShadow, outcomeReject, true, ReasonAdmissionRejected, "would_block"},
		{AdmissionShadow, outcomeUnavailable, true, ReasonAdmissionUnavailable, "would_block"},

		{AdmissionEnforce, outcomePass, true, "", ""},
		{AdmissionEnforce, outcomeWarn, true, "", ""},
		{AdmissionEnforce, outcomeReject, false, ReasonAdmissionRejected, "blocked"},
		{AdmissionEnforce, outcomeUnavailable, false, ReasonAdmissionUnavailable, "blocked"},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode)+"/"+string(tc.outcome), func(t *testing.T) {
			verdict := applyAdmissionMode(tc.mode, tc.outcome)
			assert.Equal(t, tc.wantAllow, verdict.allow)
			assert.Equal(t, tc.wantReason, verdict.reason)
			assert.Equal(t, tc.wantEvidence, verdict.evidence)
		})
	}

	t.Run("an unknown mode behaves as shadow", func(t *testing.T) {
		verdict := applyAdmissionMode(AdmissionMode("bogus"), outcomeReject)
		assert.True(t, verdict.allow, "an unvalidated mode must never block a release")
		assert.Equal(t, "would_block", verdict.evidence)
	})
}

// TestAdmissionShadowRecordsWithoutBlocking is the behaviour-preservation
// guarantee of the default mode: a rejection still lets the release through, but
// leaves evidence and counters behind.
func TestAdmissionShadowRecordsWithoutBlocking(t *testing.T) {
	eval := &fakeEvaluator{result: &vulnerability.AdmissionResult{
		Decision:      vulnerability.AdmissionReject,
		Reason:        "2 critical findings",
		PolicyVersion: "v3",
	}}
	sink := &recordingSink{}
	svc := admissionService(AdmissionShadow, eval, sink)

	require.NoError(t, svc.EvaluateArtifactAdmission(t.Context(), "sha256:abc", "sbom://ref"))

	snapshot := svc.AdmissionMetricsSnapshot()
	assert.EqualValues(t, 1, snapshot.WouldBlock)
	assert.Zero(t, snapshot.Blocked)
	assert.Zero(t, snapshot.Allowed)

	events := sink.all()
	require.Len(t, events, 1)
	assert.Equal(t, "admit_artifact", events[0].Action)
	assert.Equal(t, "would_block", events[0].Status)
	assert.Equal(t, ReasonAdmissionRejected, events[0].ChangeSummary)
	assert.Equal(t, "sha256:abc", events[0].Metadata["artifact_digest"])
	assert.Equal(t, "shadow", events[0].Metadata["mode"])
	assert.Equal(t, "v3", events[0].Metadata["policy_version"])
}

// TestAdmissionEnforceBlocks is the negative control for the switch: the same
// evaluator answer that shadow tolerated must stop the release.
func TestAdmissionEnforceBlocks(t *testing.T) {
	t.Run("policy rejection", func(t *testing.T) {
		eval := &fakeEvaluator{result: &vulnerability.AdmissionResult{Decision: vulnerability.AdmissionReject}}
		sink := &recordingSink{}
		svc := admissionService(AdmissionEnforce, eval, sink)

		err := svc.EvaluateArtifactAdmission(t.Context(), "sha256:abc", "sbom://ref")
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		assert.Contains(t, err.Error(), ReasonAdmissionRejected)

		snapshot := svc.AdmissionMetricsSnapshot()
		assert.EqualValues(t, 1, snapshot.Blocked)
		assert.Zero(t, snapshot.WouldBlock)

		events := sink.all()
		require.Len(t, events, 1)
		assert.Equal(t, "blocked", events[0].Status)
	})

	t.Run("evaluation unavailable", func(t *testing.T) {
		// No evaluator configured: this is the case the card was written for, and
		// enforce turns it into an unavailable error rather than a silent pass.
		sink := &recordingSink{}
		svc := admissionService(AdmissionEnforce, nil, sink)

		err := svc.EvaluateArtifactAdmission(t.Context(), "sha256:abc", "sbom://ref")
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		assert.Contains(t, err.Error(), ReasonAdmissionUnavailable)
		assert.EqualValues(t, 1, svc.AdmissionMetricsSnapshot().Blocked)
	})

	t.Run("evaluator failure", func(t *testing.T) {
		eval := &fakeEvaluator{err: errors.New("scanner unreachable")}
		svc := admissionService(AdmissionEnforce, eval, nil)

		err := svc.EvaluateArtifactAdmission(t.Context(), "sha256:abc", "sbom://ref")
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		assert.Contains(t, err.Error(), "scanner unreachable", "the cause stays visible to the caller")
	})
}

// TestAdmissionOffSkipsEvaluation documents the pre-TASK-105 behaviour.
func TestAdmissionOffSkipsEvaluation(t *testing.T) {
	eval := &fakeEvaluator{result: &vulnerability.AdmissionResult{Decision: vulnerability.AdmissionReject}}
	svc := admissionService(AdmissionOff, eval, nil)

	require.NoError(t, svc.EvaluateArtifactAdmission(t.Context(), "sha256:abc", "sbom://ref"))
	assert.Zero(t, eval.callCount(), "off must not evaluate at all")
	assert.EqualValues(t, 1, svc.AdmissionMetricsSnapshot().Skipped)
}

// TestAdmissionPassAndWarnAreAllowedInEveryMode keeps a warn from becoming a
// blocker: warn is the policy's "notable but acceptable" answer.
func TestAdmissionPassAndWarnAreAllowedInEveryMode(t *testing.T) {
	for _, mode := range []AdmissionMode{AdmissionOff, AdmissionShadow, AdmissionEnforce} {
		for _, decision := range []vulnerability.AdmissionDecision{vulnerability.AdmissionPass, vulnerability.AdmissionWarn} {
			t.Run(string(mode)+"/"+string(decision), func(t *testing.T) {
				eval := &fakeEvaluator{result: &vulnerability.AdmissionResult{Decision: decision}}
				svc := admissionService(mode, eval, nil)
				require.NoError(t, svc.EvaluateArtifactAdmission(t.Context(), "sha256:abc", "sbom://ref"))
			})
		}
	}
}

// TestAdmissionDefaultsToShadow covers a Service built without the policy arg:
// the zero value must be the non-blocking mode.
func TestAdmissionDefaultsToShadow(t *testing.T) {
	eval := &fakeEvaluator{result: &vulnerability.AdmissionResult{Decision: vulnerability.AdmissionReject}}
	svc := admissionService("", eval, nil)

	require.NoError(t, svc.EvaluateArtifactAdmission(t.Context(), "sha256:abc", "sbom://ref"),
		"a missing mode must not block")
	assert.EqualValues(t, 1, svc.AdmissionMetricsSnapshot().WouldBlock)
}

// REQ-042 (D): an artifact without a digest fails closed.
//
// It used to return nil ("not an admission decision") on the strength of an
// upstream invariant — the bundle contract rejects an empty image digest, so the
// branch is unreachable on a valid flow (see
// TestBundleValidationRejectsEmptyImageDigestSoAdmissionNeverSeesIt). An allow
// that rests on an upstream invariant is one broken invariant away from a silent
// pass, so the digest-less artifact is now reported as undecidable and the mode
// decides. The evaluator is never consulted: there is nothing to evaluate.
func TestAdmissionEmptyDigestFailsClosed(t *testing.T) {
	t.Run("enforce blocks", func(t *testing.T) {
		eval := &fakeEvaluator{result: &vulnerability.AdmissionResult{Decision: vulnerability.AdmissionPass}}
		svc := admissionService(AdmissionEnforce, eval, nil)

		err := svc.EvaluateArtifactAdmission(t.Context(), "", "sbom://ref")
		require.Error(t, err, "a digest-less artifact must never be allowed")
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		assert.Contains(t, err.Error(), ReasonAdmissionUnavailable)
		assert.Zero(t, eval.callCount(), "there is nothing to evaluate without a digest")
		assert.EqualValues(t, 1, svc.AdmissionMetricsSnapshot().Blocked)
	})

	t.Run("shadow records without counting an allow", func(t *testing.T) {
		eval := &fakeEvaluator{result: &vulnerability.AdmissionResult{Decision: vulnerability.AdmissionPass}}
		sink := &recordingSink{}
		svc := admissionService(AdmissionShadow, eval, sink)

		require.NoError(t, svc.EvaluateArtifactAdmission(t.Context(), "", "sbom://ref"))

		snapshot := svc.AdmissionMetricsSnapshot()
		assert.EqualValues(t, 1, snapshot.WouldBlock)
		assert.Zero(t, snapshot.Allowed)
		require.Len(t, sink.all(), 1, "an undecidable artifact must leave evidence")
		assert.Zero(t, eval.callCount())
	})

	t.Run("off still skips", func(t *testing.T) {
		eval := &fakeEvaluator{result: &vulnerability.AdmissionResult{Decision: vulnerability.AdmissionReject}}
		svc := admissionService(AdmissionOff, eval, nil)

		require.NoError(t, svc.EvaluateArtifactAdmission(t.Context(), "", "sbom://ref"))
		assert.EqualValues(t, 1, svc.AdmissionMetricsSnapshot().Skipped)
		assert.Zero(t, eval.callCount())
	})
}
