package preflight

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/ndzuki/release-manager/internal/store"
)

// ResolvedRoute is the target selected for one artifact.
type ResolvedRoute struct {
	Mode      store.ArtifactMode
	TargetURI string
}

// ResolveRoute applies the longest matching explicit prefix rule.
func ResolveRoute(
	artifactType store.ArtifactType,
	sourceRef string,
	routes []*store.ClusterRoute,
) (ResolvedRoute, error) {
	var matched *store.ClusterRoute
	for _, route := range routes {
		if route == nil || route.ArtifactType != artifactType {
			continue
		}
		if !prefixMatches(sourceRef, route.SourcePrefix) {
			continue
		}
		if matched == nil || len(route.SourcePrefix) > len(matched.SourcePrefix) {
			matched = route
		}
	}

	if matched == nil {
		return ResolvedRoute{}, fmt.Errorf("%s: no %s route matches %q", ErrorRoutingNoMatch, artifactType, sourceRef)
	}

	return ResolvedRoute{
		Mode:      matched.Mode,
		TargetURI: matched.TargetPrefix + strings.TrimPrefix(sourceRef, matched.SourcePrefix),
	}, nil
}

// RoutingVersion returns a stable content hash for a cluster route snapshot.
func RoutingVersion(routes []*store.ClusterRoute) string {
	parts := make([]string, 0, len(routes))
	for _, route := range routes {
		if route == nil {
			continue
		}
		parts = append(parts, strings.Join([]string{
			string(route.ArtifactType),
			string(route.Mode),
			route.SourcePrefix,
			route.TargetPrefix,
		}, "\x00"))
	}
	sort.Strings(parts)
	digest := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return fmt.Sprintf("sha256:%x", digest)
}

func prefixMatches(sourceRef, prefix string) bool {
	if prefix == "" || !strings.HasPrefix(sourceRef, prefix) {
		return false
	}
	if len(sourceRef) == len(prefix) || strings.HasSuffix(prefix, "/") {
		return true
	}
	return sourceRef[len(prefix)] == '/'
}
