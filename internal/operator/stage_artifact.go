package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
)

// ErrArtifactDigestMismatch marks a chart archive whose content digest does not
// match the bundle's chart_digest.
var ErrArtifactDigestMismatch = errors.New("artifact digest mismatch")

// ArtifactLocator resolves a chart reference to the raw bytes of the chart
// archive it points at, without loading or rendering it.
//
// It is deliberately narrower than ChartLocator: the artifact stage only needs
// the archive bytes (the digest is computed over them), and asking for a loaded
// chart would tie the artifact check to the render path. *helmengine.RealEngine
// implements it.
type ArtifactLocator interface {
	LocateChartArchive(chartRef, chartVersion string, plainHTTP bool) ([]byte, error)
}

// ArtifactStageExecutor runs the preflight artifact stage: it resolves the
// approved chart to its archive bytes and verifies that the archive's content
// digest matches the bundle's chart_digest (ADR-024).
//
// The parity is over the chart *archive bytes*, not the OCI manifest digest:
// the bundle's chart_digest is produced that way by the packaging path
// (internal/devfixture/bundle.go archiveDigest) and by the install path's
// verification (helmengine digestMatches), so comparing anything else would
// disagree with the value the bundle actually carries.
//
// Every outcome fails closed. A missing bundle, a missing chart_digest, an
// unreachable archive, or a digest mismatch all return an error rather than a
// "passed" result: a stage exists to prove a check ran, and a check that could
// not run must never be reported as a pass.
type ArtifactStageExecutor struct {
	locator   ArtifactLocator
	plainHTTP bool
	logger    *slog.Logger
}

// NewArtifactStageExecutor builds the artifact stage executor. plainHTTP
// mirrors the engine's registry setting so a plain-HTTP dev fixture registry is
// resolved exactly as the install path resolves it.
func NewArtifactStageExecutor(locator ArtifactLocator, plainHTTP bool, logger *slog.Logger) *ArtifactStageExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &ArtifactStageExecutor{locator: locator, plainHTTP: plainHTTP, logger: logger}
}

// artifactStageResult is the stage's reported result. Status is "passed" only
// when the archive digest matched; every other outcome is an error, so a
// consumer never has to interpret a failure-shaped result.
type artifactStageResult struct {
	Status      string `json:"status"`
	ChartDigest string `json:"chart_digest"`
	Detail      string `json:"detail,omitempty"`
}

// ExecuteStage resolves the command's chart archive and verifies its digest
// against the bundle's chart_digest.
func (e *ArtifactStageExecutor) ExecuteStage(ctx context.Context, command *operatorv1.Command) (string, error) {
	if e == nil || e.locator == nil {
		return "", fmt.Errorf("artifact stage requires a chart archive locator")
	}
	if command == nil {
		return "", fmt.Errorf("artifact stage requires a command")
	}
	bundle := command.GetBundle()
	if bundle == nil {
		return "", fmt.Errorf("artifact stage requires a bundle")
	}
	chartRef := strings.TrimSpace(bundle.GetChartRef())
	if chartRef == "" {
		return "", fmt.Errorf("artifact stage requires a chart reference")
	}
	// Fail closed on a missing digest: there is nothing to verify against, and
	// treating "no digest" as a pass would admit an unverified artifact.
	expectedDigest := strings.TrimSpace(bundle.GetChartDigest())
	if expectedDigest == "" {
		return "", fmt.Errorf("artifact stage requires the bundle chart digest")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	archive, err := e.locator.LocateChartArchive(chartRef, bundle.GetChartVersion(), e.plainHTTP)
	if err != nil {
		return "", fmt.Errorf("artifact stage: %w", err)
	}
	if len(archive) == 0 {
		// An empty file is not a chart archive; reporting a digest match on it
		// would be a vacuous pass.
		return "", fmt.Errorf("artifact stage: chart archive for %q is empty", chartRef)
	}
	actualDigest := chartArchiveDigest(archive)
	if !digestValueMatches(expectedDigest, actualDigest) {
		return "", fmt.Errorf("artifact stage: chart archive %q digest mismatch: %w",
			chartRef, ErrArtifactDigestMismatch)
	}
	encoded, err := json.Marshal(artifactStageResult{
		Status:      "passed",
		ChartDigest: actualDigest,
		Detail:      "chart archive digest matches the bundle chart_digest",
	})
	if err != nil {
		return "", fmt.Errorf("artifact stage: encode result: %w", err)
	}
	e.logger.Info("preflight artifact stage passed",
		"operation_id", command.GetOperationId(), "chart_digest", actualDigest)
	return string(encoded), nil
}

// chartArchiveDigest returns the content digest of a chart archive in the
// canonical "sha256:"-prefixed lowercase hex form the bundle uses.
func chartArchiveDigest(archive []byte) string {
	sum := sha256.Sum256(archive)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// digestValueMatches compares a bundle digest against a computed one with the
// same semantics as the install path's verification (helmengine.digestMatches):
// a lowercase "sha256:" prefix is optional, and the hex comparison is
// case-insensitive. The prefix strip is deliberately case-sensitive, exactly as
// the install path does it, so the artifact stage never accepts a spelling the
// install would then reject.
func digestValueMatches(expected, actual string) bool {
	return strings.EqualFold(
		strings.TrimPrefix(strings.TrimSpace(expected), "sha256:"),
		strings.TrimPrefix(strings.TrimSpace(actual), "sha256:"),
	)
}

var _ StageExecutor = (*ArtifactStageExecutor)(nil)
