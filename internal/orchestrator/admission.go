package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"connectrpc.com/connect"

	"github.com/ndzuki/release-manager/internal/audit"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/vulnerability"
)

// AdmissionMode decides how the artifact admission step treats the vulnerability
// evaluator's answer (TASK-105).
type AdmissionMode string

const (
	// AdmissionOff skips the evaluation entirely: the pre-TASK-105 behaviour.
	AdmissionOff AdmissionMode = "off"
	// AdmissionShadow runs the evaluation and records what would have been
	// blocked, but never changes the outcome. It is the default because the step
	// is newly wired: an operator quantifies the blast radius first, then opts in.
	AdmissionShadow AdmissionMode = "shadow"
	// AdmissionEnforce blocks on a rejection or on an unavailable evaluation.
	AdmissionEnforce AdmissionMode = "enforce"
)

// admissionOutcome is the evaluator's answer, normalised so the mode matrix has a
// single input shape.
type admissionOutcome string

const (
	outcomePass        admissionOutcome = "pass"
	outcomeWarn        admissionOutcome = "warn"
	outcomeReject      admissionOutcome = "reject"
	outcomeUnavailable admissionOutcome = "unavailable"
)

// Stable reason codes. They land in the audit trail and in the API error, so they
// are part of the observable contract.
const (
	// ReasonAdmissionUnavailable means the decision could not be made: no
	// evaluator is configured, or the evaluation failed.
	ReasonAdmissionUnavailable = "vulnerability_policy_unavailable"
	// ReasonAdmissionRejected means the evaluator answered reject.
	ReasonAdmissionRejected = "vulnerability_policy_failed"
)

// admissionVerdict is what a mode makes of an outcome.
type admissionVerdict struct {
	allow bool
	// reason is the stable reason code, empty when there is nothing to record.
	reason string
	// evidence is "would_block", "blocked", or empty when the outcome is allowed
	// silently (a pass, a warn, or a skipped evaluation).
	evidence string
}

// applyAdmissionMode maps (mode, outcome) to a verdict. It is pure, so the whole
// matrix is testable without a store, a scanner or a policy.
//
// pass and warn are allowed in every mode: warn is the policy saying "this is
// acceptable but noteworthy", not a rejection.
func applyAdmissionMode(mode AdmissionMode, outcome admissionOutcome) admissionVerdict {
	switch mode {
	case AdmissionOff:
		return admissionVerdict{allow: true}
	case AdmissionEnforce:
		switch outcome {
		case outcomeReject:
			return admissionVerdict{allow: false, reason: ReasonAdmissionRejected, evidence: "blocked"}
		case outcomeUnavailable:
			return admissionVerdict{allow: false, reason: ReasonAdmissionUnavailable, evidence: "blocked"}
		default:
			return admissionVerdict{allow: true}
		}
	default:
		// AdmissionShadow, and any value that reached the service without passing
		// config validation: shadow is the fail-safe choice, because it cannot
		// block a release.
		switch outcome {
		case outcomeReject:
			return admissionVerdict{allow: true, reason: ReasonAdmissionRejected, evidence: "would_block"}
		case outcomeUnavailable:
			return admissionVerdict{allow: true, reason: ReasonAdmissionUnavailable, evidence: "would_block"}
		default:
			return admissionVerdict{allow: true}
		}
	}
}

// AdmissionMetrics counts admission decisions. The orchestrator has no Prometheus
// scrape surface, so the counters are exposed as a snapshot, mirroring the audit
// emitter's convention in this repository.
type AdmissionMetrics struct {
	allowed    atomic.Uint64
	wouldBlock atomic.Uint64
	blocked    atomic.Uint64
	skipped    atomic.Uint64
}

// AdmissionMetricsSnapshot is a point-in-time copy of the counters.
type AdmissionMetricsSnapshot struct {
	Allowed    uint64 `json:"allowed"`
	WouldBlock uint64 `json:"would_block"`
	Blocked    uint64 `json:"blocked"`
	Skipped    uint64 `json:"skipped"`
}

// Snapshot returns the current counters.
func (m *AdmissionMetrics) Snapshot() AdmissionMetricsSnapshot {
	if m == nil {
		return AdmissionMetricsSnapshot{}
	}
	return AdmissionMetricsSnapshot{
		Allowed:    m.allowed.Load(),
		WouldBlock: m.wouldBlock.Load(),
		Blocked:    m.blocked.Load(),
		Skipped:    m.skipped.Load(),
	}
}

// artifactEvaluator is the seam the admission step depends on, so tests inject a
// fake instead of building a real evaluator (store + scanner + policy).
type artifactEvaluator interface {
	Evaluate(ctx context.Context, artifactDigest, sbomRef string) (*vulnerability.AdmissionResult, error)
}

// EvaluateArtifactAdmission applies the configured mode to one artifact's
// vulnerability evaluation.
//
// A nil error means "the release may proceed". When the mode blocks, the returned
// error is a Connect error whose code distinguishes an unavailable decision
// (unavailable) from a policy rejection (failed_precondition), matching how the
// trust-verification path reports the same two classes.
func (s *Service) EvaluateArtifactAdmission(ctx context.Context, artifactDigest, sbomRef string) error {
	mode := s.admissionMode
	if mode == "" {
		mode = AdmissionShadow
	}
	if mode == AdmissionOff {
		s.admissionMetrics().skipped.Add(1)
		return nil
	}
	if artifactDigest == "" {
		// Nothing to evaluate: an artifact without a digest is rejected earlier by
		// the bundle contract, so this is not an admission decision.
		return nil
	}

	outcome, policyVersion, evaluateErr := s.evaluateArtifact(ctx, artifactDigest, sbomRef)
	verdict := applyAdmissionMode(mode, outcome)

	switch verdict.evidence {
	case "would_block":
		s.admissionMetrics().wouldBlock.Add(1)
		s.logger.Warn("artifact admission would block",
			"artifact_digest", artifactDigest,
			"mode", string(mode),
			"reason", verdict.reason,
			"policy_version", policyVersion,
		)
		s.emitAdmissionAudit(ctx, artifactDigest, sbomRef, mode, verdict, policyVersion)
		return nil
	case "blocked":
		s.admissionMetrics().blocked.Add(1)
		s.logger.Warn("artifact admission blocked",
			"artifact_digest", artifactDigest,
			"mode", string(mode),
			"reason", verdict.reason,
			"policy_version", policyVersion,
		)
		s.emitAdmissionAudit(ctx, artifactDigest, sbomRef, mode, verdict, policyVersion)
		code := connect.CodeFailedPrecondition
		if verdict.reason == ReasonAdmissionUnavailable {
			code = connect.CodeUnavailable
		}
		if evaluateErr != nil {
			return connect.NewError(code, fmt.Errorf("%s: %w", verdict.reason, evaluateErr))
		}
		return connect.NewError(code, errors.New(verdict.reason))
	default:
		s.admissionMetrics().allowed.Add(1)
		return nil
	}
}

// evaluateArtifact runs the evaluator when one is configured and normalises the
// answer. A missing evaluator and an evaluation failure are both "unavailable":
// the mode decides whether that is fatal, and the caller must not have to guess.
func (s *Service) evaluateArtifact(
	ctx context.Context,
	artifactDigest, sbomRef string,
) (admissionOutcome, string, error) {
	if s.vulnEval == nil {
		return outcomeUnavailable, "", errors.New("no vulnerability evaluator is configured")
	}
	result, err := s.vulnEval.Evaluate(ctx, artifactDigest, sbomRef)
	if err != nil {
		return outcomeUnavailable, "", fmt.Errorf("vulnerability evaluation: %w", err)
	}
	if result == nil {
		return outcomeUnavailable, "", errors.New("vulnerability evaluation returned no result")
	}
	switch result.Decision {
	case vulnerability.AdmissionPass:
		return outcomePass, result.PolicyVersion, nil
	case vulnerability.AdmissionWarn:
		return outcomeWarn, result.PolicyVersion, nil
	default:
		return outcomeReject, result.PolicyVersion, nil
	}
}

// emitAdmissionAudit records a would-block or blocked decision. The metadata
// carries references and reason codes only — never a finding's contents.
func (s *Service) emitAdmissionAudit(
	ctx context.Context,
	artifactDigest, sbomRef string,
	mode AdmissionMode,
	verdict admissionVerdict,
	policyVersion string,
) {
	actorKind := store.AuditActorUser
	actorID, organizationID, role := "", "", ""
	if actor, ok := authctx.ActorFromContext(ctx); ok {
		actorID = actor.UserID
		organizationID = actor.OrganizationID
		if actor.Service != "" {
			actorKind = store.AuditActorService
			actorID = actor.Service
		}
		if len(actor.Roles) > 0 {
			role = actor.Roles[0]
		}
	}
	s.emitAudit(audit.NewEvent(
		actorKind,
		actorID,
		organizationID,
		role,
		"release_bundle",
		artifactDigest,
		"admit_artifact",
		verdict.evidence,
		verdict.reason,
		map[string]string{
			"artifact_digest": artifactDigest,
			"sbom_ref":        sbomRef,
			"mode":            string(mode),
			"reason":          verdict.reason,
			"policy_version":  policyVersion,
		},
	))
}

// admissionMetrics returns the counters, tolerating a Service built without them
// (tests construct the struct directly).
func (s *Service) admissionMetrics() *AdmissionMetrics {
	if s.admissionCounters == nil {
		s.admissionCounters = &AdmissionMetrics{}
	}
	return s.admissionCounters
}

// AdmissionMetricsSnapshot exposes the counters for diagnostics and tests.
func (s *Service) AdmissionMetricsSnapshot() AdmissionMetricsSnapshot {
	return s.admissionMetrics().Snapshot()
}
