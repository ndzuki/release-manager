package orchestrator

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/vulnerability"
)

// REQ-042 gate honesty.
//
// The live admission step is EvaluateArtifactAdmission, called from the bundle
// submission path (service.go). These tests pin the property the requirement is
// about: "the scanner is unavailable" is never reported as a pass, on either
// mode. An unconfigured or broken gate must not be launderable into an allow.
//
// They are the counterweight to the tempting shortcut of wiring a
// vulnerability.NoopScanner-backed evaluator to "make the gate pass" — see the
// NoopScanner doc for why that is forbidden and why a real scanner cannot be
// shelled out either (`make sdk-check` rejects `import "os/exec"`; ADR-004 and
// ADR-001 own the cluster boundary).

// TestAdmissionUnavailableNeverPassesInEnforce covers every shape of "we could
// not decide" through the real service method: a nil evaluator (the production
// default), a failing evaluator, and an evaluator that answers with no result.
// Enforce must block each of them with the unavailable code.
func TestAdmissionUnavailableNeverPassesInEnforce(t *testing.T) {
	cases := []struct {
		name string
		eval artifactEvaluator
	}{
		{name: "no evaluator configured", eval: nil},
		{name: "evaluator failed", eval: &fakeEvaluator{err: errors.New("scanner backend unreachable")}},
		{name: "evaluator returned no result", eval: &fakeEvaluator{result: nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := admissionService(AdmissionEnforce, tc.eval, nil)

			err := svc.EvaluateArtifactAdmission(t.Context(), "sha256:abc", "sbom://ref")
			require.Error(t, err, "an unavailable decision must never allow the release")
			assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
			assert.Contains(t, err.Error(), ReasonAdmissionUnavailable)

			snapshot := svc.AdmissionMetricsSnapshot()
			assert.EqualValues(t, 1, snapshot.Blocked)
			assert.Zero(t, snapshot.Allowed, "unavailable must never be counted as allowed")
		})
	}
}

// TestAdmissionUnavailableIsRecordedInShadow pins "never silent": the default
// mode does not block, but it must still record the unavailable outcome (metric,
// warn log and audit event) instead of letting it pass unobserved.
func TestAdmissionUnavailableIsRecordedInShadow(t *testing.T) {
	sink := &recordingSink{}
	svc := admissionService(AdmissionShadow, nil, sink)

	require.NoError(t, svc.EvaluateArtifactAdmission(t.Context(), "sha256:abc", "sbom://ref"))

	snapshot := svc.AdmissionMetricsSnapshot()
	assert.EqualValues(t, 1, snapshot.WouldBlock)
	assert.Zero(t, snapshot.Allowed, "shadow must not count an unavailable decision as allowed")

	events := sink.all()
	require.Len(t, events, 1, "shadow must leave evidence for every unavailable decision")
	assert.Equal(t, "would_block", events[0].Status)
	assert.Equal(t, ReasonAdmissionUnavailable, events[0].ChangeSummary)
	assert.Equal(t, "shadow", events[0].Metadata["mode"])
}

// noResultStore is a vulnerability.ResultStore with nothing persisted: it stands
// in for an evaluator whose scanner has never produced a result.
type noResultStore struct{}

func (noResultStore) GetLatest(context.Context, string, string) (*vulnerability.ScannerResult, error) {
	return nil, nil
}

func (noResultStore) SaveResult(context.Context, *vulnerability.ScannerResult) error { return nil }

func (noResultStore) GetExceptions(context.Context, string) ([]vulnerability.Exception, error) {
	return nil, nil
}

// TestAdmissionNoopScannerEvaluatorDoesNotPassUnscannedArtifact is the guard
// against the shortcut REQ-042 exists to prevent: attaching a real
// vulnerability.Evaluator backed by vulnerability.NewNoopScanner so the gate
// stops complaining. With nothing actually scanned, the evaluator rejects and
// enforce blocks; shadow records a would-block without counting an allow.
func TestAdmissionNoopScannerEvaluatorDoesNotPassUnscannedArtifact(t *testing.T) {
	eval := vulnerability.NewEvaluator(
		noResultStore{},
		vulnerability.NewNoopScanner("trivy"),
		vulnerability.DefaultProductionPolicy(),
	)

	t.Run("enforce blocks", func(t *testing.T) {
		svc := admissionService(AdmissionEnforce, eval, nil)

		err := svc.EvaluateArtifactAdmission(t.Context(), "sha256:abc", "sbom://ref")
		require.Error(t, err, "a NoopScanner-backed evaluator must not turn an unscanned artifact into a pass")
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		assert.Contains(t, err.Error(), ReasonAdmissionRejected)

		snapshot := svc.AdmissionMetricsSnapshot()
		assert.EqualValues(t, 1, snapshot.Blocked)
		assert.Zero(t, snapshot.Allowed)
	})

	t.Run("shadow records without counting an allow", func(t *testing.T) {
		sink := &recordingSink{}
		svc := admissionService(AdmissionShadow, eval, sink)

		require.NoError(t, svc.EvaluateArtifactAdmission(t.Context(), "sha256:abc", "sbom://ref"))

		snapshot := svc.AdmissionMetricsSnapshot()
		assert.EqualValues(t, 1, snapshot.WouldBlock)
		assert.Zero(t, snapshot.Allowed)
		require.Len(t, sink.all(), 1)
	})
}
