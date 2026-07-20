// Package preflight validates release bundle artifacts before queueing an operation.
package preflight

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/trust"
)

// ErrorCode is a stable artifact preflight failure code.
type ErrorCode string

const (
	ErrorNone                  ErrorCode = ""
	ErrorRoutingNoMatch        ErrorCode = "routing_no_match"
	ErrorArtifactNotFound      ErrorCode = "artifact_not_found"
	ErrorRegistryUnauthorized  ErrorCode = "registry_unauthorized"
	ErrorDigestMismatch        ErrorCode = "digest_mismatch"
	ErrorSignatureInvalid      ErrorCode = "signature_invalid"
	ErrorSignatureMissing      ErrorCode = "signature_missing"
	ErrorDependencyUnavailable ErrorCode = "dependency_unavailable"
)

var (
	ErrArtifactNotFound      = errors.New("artifact not found")
	ErrRegistryUnauthorized  = errors.New("registry unauthorized")
	ErrDependencyUnavailable = errors.New("preflight dependency unavailable")
)

// Input contains the immutable data required for one preflight run.
type Input struct {
	OperationID       string
	ClusterID         string
	Bundle            *store.ReleaseBundle
	Routes            []*store.ClusterRoute
	TrustPolicy       store.TrustPolicy
	SBOMPolicyVersion string
	SignatureRef      *commonv1.SignatureRef
	AllowedHosts      []string
}

// ArtifactResult is the independent result for one chart or image.
type ArtifactResult struct {
	Type            store.ArtifactType       `json:"type"`
	Ref             string                   `json:"ref"`
	ExpectedDigest  string                   `json:"expected_digest"`
	ResolvedURI     string                   `json:"resolved_uri,omitempty"`
	ResolvedDigest  string                   `json:"resolved_digest,omitempty"`
	DigestParity    bool                     `json:"digest_parity"`
	RouteMode       store.ArtifactMode       `json:"route_mode,omitempty"`
	SignatureStatus store.VerificationStatus `json:"signature_status,omitempty"`
	ErrorCode       ErrorCode                `json:"error_code,omitempty"`
	Summary         string                   `json:"summary,omitempty"`
	DurationMS      int64                    `json:"duration_ms"`
}

// Output is the aggregate result persisted for an idempotent preflight key.
type Output struct {
	OperationID    string           `json:"operation_id"`
	RoutingVersion string           `json:"routing_version"`
	BundleDigest   string           `json:"bundle_digest"`
	Results        []ArtifactResult `json:"results"`
	Passed         bool             `json:"passed"`
	Reused         bool             `json:"reused"`
	DurationMS     int64            `json:"duration_ms"`
}

// Resolver resolves a routed artifact to the digest served by its target source.
type Resolver interface {
	ResolveDigest(ctx context.Context, artifactType store.ArtifactType, targetURI string) (string, error)
}

// Runner executes artifact preflight and returns a stable aggregate result.
type Runner interface {
	Run(ctx context.Context, in Input) (*Output, error)
}

// Service implements artifact preflight orchestration.
type Service struct {
	results  store.PreflightStore
	verifier trust.Verifier
	resolver Resolver
	logger   Logger
}

// Logger is the subset of slog.Logger behavior needed by preflight.
type Logger interface {
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
}

// New creates a preflight runner with explicit persistence and dependencies.
func New(results store.PreflightStore, verifier trust.Verifier, resolver Resolver, logger Logger) *Service {
	if logger == nil {
		logger = discardLogger{}
	}
	return &Service{
		results:  results,
		verifier: verifier,
		resolver: resolver,
		logger:   logger,
	}
}

func (o *Output) FailureSummary() string {
	for i := range o.Results {
		if o.Results[i].ErrorCode != ErrorNone {
			if o.Results[i].Summary != "" {
				return fmt.Sprintf("%s: %s", o.Results[i].ErrorCode, o.Results[i].Summary)
			}
			return string(o.Results[i].ErrorCode)
		}
	}
	return ""
}

func (o *Output) hasFailures() bool {
	for i := range o.Results {
		if o.Results[i].ErrorCode != ErrorNone {
			return true
		}
	}
	return false
}

func validateInput(in Input) error {
	switch {
	case strings.TrimSpace(in.OperationID) == "":
		return fmt.Errorf("operation id is required")
	case strings.TrimSpace(in.ClusterID) == "":
		return fmt.Errorf("cluster id is required")
	case in.Bundle == nil:
		return fmt.Errorf("release bundle is required")
	case strings.TrimSpace(in.Bundle.DigestValue) == "":
		return fmt.Errorf("release bundle digest is required")
	}
	return nil
}

func routeAllowed(targetURI string, allowedHosts []string) error {
	if len(allowedHosts) == 0 {
		return nil
	}

	parsed, err := url.Parse(targetURI)
	if err != nil {
		return fmt.Errorf("parse routed target: %w", err)
	}
	hostname := strings.ToLower(parsed.Hostname())
	for _, allowed := range allowedHosts {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == hostname {
			return nil
		}
	}
	return fmt.Errorf("target host %q is not allowed", hostname)
}

type discardLogger struct{}

func (discardLogger) Debug(string, ...any) {}
func (discardLogger) Warn(string, ...any)  {}

func durationMS(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
