package orchestrator

import (
	"fmt"
	"strings"

	"github.com/ndzuki/release-manager/internal/store"
)

// ValidateRouteConfig checks that a cluster route configuration is valid.
// AC-014-02: image/chart routes are independently validated.
// AC-014-03: invalid prefix rules are rejected.
func ValidateRouteConfig(artifactType store.ArtifactType, mode store.ArtifactMode, sourcePrefix, targetPrefix string) error {
	if !artifactType.Valid() {
		return fmt.Errorf("invalid artifact_type %q: must be %q or %q", artifactType, store.ArtifactImage, store.ArtifactChart)
	}

	if !mode.Valid() {
		return fmt.Errorf("invalid mode %q", mode)
	}

	// Chart routes do not support pull_through_cache yet (REQ-014 constraint).
	if artifactType == store.ArtifactChart && mode == store.ModePullThroughCache {
		return fmt.Errorf("mode %q is not supported for artifact type %q", mode, artifactType)
	}

	sourcePrefix = strings.TrimSpace(sourcePrefix)
	targetPrefix = strings.TrimSpace(targetPrefix)

	if sourcePrefix == "" {
		return fmt.Errorf("source_prefix must not be empty")
	}
	if targetPrefix == "" {
		return fmt.Errorf("target_prefix must not be empty")
	}

	// Prefixes must not contain whitespace or control characters.
	if containsWhitespace(sourcePrefix) {
		return fmt.Errorf("source_prefix must not contain whitespace")
	}
	if containsWhitespace(targetPrefix) {
		return fmt.Errorf("target_prefix must not contain whitespace")
	}

	return nil
}

// DetectConflictingRoutes checks whether newRoute conflicts with any existing route.
// A conflict occurs when two routes share the same (cluster_id, artifact_type, source_prefix).
// AC-014-03: conflicting prefix rules are rejected.
func DetectConflictingRoutes(existing []*store.ClusterRoute, newRoute *store.ClusterRoute) error {
	for _, r := range existing {
		if r.ID == newRoute.ID {
			continue // Same route is being updated, not a conflict.
		}
		if r.ArtifactType == newRoute.ArtifactType && r.SourcePrefix == newRoute.SourcePrefix {
			return fmt.Errorf(
				"conflicting route: cluster %q already has a route for %q with source_prefix %q (route %q)",
				newRoute.ClusterID, newRoute.ArtifactType, newRoute.SourcePrefix, r.ID,
			)
		}
	}
	return nil
}

func containsWhitespace(s string) bool {
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return true
		}
	}
	return false
}
