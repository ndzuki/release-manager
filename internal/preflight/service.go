package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/trust"
)

// Run validates every chart and image in a release bundle and caches the aggregate result.
func (s *Service) Run(ctx context.Context, in Input) (*Output, error) {
	if err := validateInput(in); err != nil {
		return nil, err
	}
	if s.results == nil {
		return nil, fmt.Errorf("preflight result store is required")
	}
	if s.resolver == nil {
		return nil, fmt.Errorf("artifact resolver is required")
	}

	startedAt := time.Now()
	routingVersion := RoutingVersion(in.Routes)
	key := store.PreflightCacheKey{
		OperationID:        in.OperationID,
		RoutingVersion:     routingVersion,
		BundleDigest:       in.Bundle.DigestValue,
		TrustPolicyVersion: in.TrustPolicy.PolicyVersion,
		SBOMPolicyVersion:  in.SBOMPolicyVersion,
	}

	cached, err := s.results.GetByKey(ctx, key)
	if err == nil {
		out := &Output{}
		if err := json.Unmarshal(cached.ResultJSON, out); err != nil {
			return nil, fmt.Errorf("decode cached preflight result: %w", err)
		}
		out.Reused = true
		s.logger.Debug("preflight result reused", "operation_id", in.OperationID)
		return out, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("lookup preflight result: %w", err)
	}

	artifacts := bundleArtifacts(in.Bundle)
	results := make([]ArtifactResult, 0, len(artifacts))
	for _, artifact := range artifacts {
		results = append(results, s.checkArtifact(ctx, in, artifact))
	}

	out := &Output{
		OperationID:    in.OperationID,
		RoutingVersion: routingVersion,
		BundleDigest:   in.Bundle.DigestValue,
		Results:        results,
		Passed:         true,
		DurationMS:     durationMS(startedAt),
	}
	out.Passed = !out.hasFailures()

	resultJSON, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode preflight result: %w", err)
	}
	if err := s.results.Create(ctx, &store.PreflightRecord{
		Key:        key,
		ResultJSON: resultJSON,
	}); err != nil {
		return nil, fmt.Errorf("persist preflight result: %w", err)
	}

	return out, nil
}

type artifact struct {
	artifactType   store.ArtifactType
	ref            string
	expectedDigest string
}

func bundleArtifacts(bundle *store.ReleaseBundle) []artifact {
	artifacts := make([]artifact, 0, 1+len(bundle.Images))
	if bundle.ChartRef != "" || bundle.ChartDigest != "" {
		artifacts = append(artifacts, artifact{
			artifactType:   store.ArtifactChart,
			ref:            bundle.ChartRef,
			expectedDigest: bundle.ChartDigest,
		})
	}
	for _, image := range bundle.Images {
		artifacts = append(artifacts, artifact{
			artifactType:   store.ArtifactImage,
			ref:            image.Ref,
			expectedDigest: image.Digest,
		})
	}
	return artifacts
}

func (s *Service) checkArtifact(ctx context.Context, in Input, artifact artifact) (result ArtifactResult) {
	startedAt := time.Now()
	result = ArtifactResult{
		Type:           artifact.artifactType,
		Ref:            artifact.ref,
		ExpectedDigest: artifact.expectedDigest,
	}
	defer func() {
		result.DurationMS = durationMS(startedAt)
	}()

	if strings.TrimSpace(artifact.ref) == "" || strings.TrimSpace(artifact.expectedDigest) == "" {
		return failedArtifact(result, ErrorArtifactNotFound, "artifact ref and digest are required")
	}

	route, err := ResolveRoute(artifact.artifactType, artifact.ref, in.Routes)
	if err != nil {
		return failedArtifact(result, ErrorRoutingNoMatch, err.Error())
	}
	result.RouteMode = route.Mode
	result.ResolvedURI = route.TargetURI

	if err := routeAllowed(route.TargetURI, in.AllowedHosts); err != nil {
		return failedArtifact(result, ErrorRoutingNoMatch, err.Error())
	}

	resolvedDigest, err := s.resolver.ResolveDigest(ctx, artifact.artifactType, route.TargetURI)
	if err != nil {
		return failedArtifact(result, resolverErrorCode(err), err.Error())
	}
	result.ResolvedDigest = resolvedDigest
	result.DigestParity = resolvedDigest == artifact.expectedDigest
	if !result.DigestParity {
		return failedArtifact(
			result,
			ErrorDigestMismatch,
			fmt.Sprintf("expected %s, resolved %s", artifact.expectedDigest, resolvedDigest),
		)
	}

	if s.verifier == nil {
		return failedArtifact(result, ErrorDependencyUnavailable, "trust verifier is required")
	}
	verified, err := s.verifier.Verify(ctx, trust.Input{
		Digest:       resolvedDigest,
		SignatureRef: in.SignatureRef,
		Policy:       in.TrustPolicy,
	})
	if err != nil {
		return failedArtifact(result, ErrorDependencyUnavailable, err.Error())
	}
	result.SignatureStatus = verified.Status
	result.Summary = verified.Summary

	switch verified.Status {
	case store.VerificationTrusted, store.VerificationPolicyWarning:
		return result
	case store.VerificationSignatureMissing:
		return failedArtifact(result, ErrorSignatureMissing, verified.Summary)
	case store.VerificationVerificationUnavailable:
		return failedArtifact(result, ErrorDependencyUnavailable, verified.Summary)
	default:
		return failedArtifact(result, ErrorSignatureInvalid, verified.Summary)
	}
}

func failedArtifact(result ArtifactResult, code ErrorCode, summary string) ArtifactResult {
	result.ErrorCode = code
	result.Summary = summary
	return result
}

func resolverErrorCode(err error) ErrorCode {
	switch {
	case errors.Is(err, ErrArtifactNotFound):
		return ErrorArtifactNotFound
	case errors.Is(err, ErrRegistryUnauthorized):
		return ErrorRegistryUnauthorized
	default:
		return ErrorDependencyUnavailable
	}
}

var _ Runner = (*Service)(nil)
