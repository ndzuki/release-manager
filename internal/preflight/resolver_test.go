package preflight

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestResolveRoute_ExactChartMatch(t *testing.T) {
	routes := []*store.ClusterRoute{
		{
			ArtifactType: store.ArtifactChart,
			Mode:         store.ModeDirect,
			SourcePrefix: "charts.helm.sh/stable/",
			TargetPrefix: "registry.internal/charts/",
		},
	}

	result, err := ResolveRoute(store.ArtifactChart, "charts.helm.sh/stable/nginx", routes)
	assert.NoError(t, err)
	assert.Equal(t, store.ModeDirect, result.Mode)
	assert.Equal(t, "registry.internal/charts/nginx", result.TargetURI)
}

func TestResolveRoute_ImageReplicated(t *testing.T) {
	routes := []*store.ClusterRoute{
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeReplicated,
			SourcePrefix: "docker.io/myorg/",
			TargetPrefix: "oci://registry.internal/replicated/",
		},
	}

	result, err := ResolveRoute(store.ArtifactImage, "docker.io/myorg/app:v1.0", routes)
	assert.NoError(t, err)
	assert.Equal(t, store.ModeReplicated, result.Mode)
	assert.Equal(t, "oci://registry.internal/replicated/app:v1.0", result.TargetURI)
}

func TestResolveRoute_LongestPrefixWins(t *testing.T) {
	routes := []*store.ClusterRoute{
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeDirect,
			SourcePrefix: "docker.io/",
			TargetPrefix: "reg.io/generic/",
		},
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeReplicated,
			SourcePrefix: "docker.io/myorg/",
			TargetPrefix: "oci://reg.io/replicated/",
		},
	}

	result, err := ResolveRoute(store.ArtifactImage, "docker.io/myorg/app", routes)
	assert.NoError(t, err)
	assert.Equal(t, store.ModeReplicated, result.Mode)
	assert.Equal(t, "oci://reg.io/replicated/app", result.TargetURI)
}

// AC-045-02: No route rules → routing_no_match, no fallback to direct.
func TestResolveRoute_NoMatchReturnsError(t *testing.T) {
	routes := []*store.ClusterRoute{
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeDirect,
			SourcePrefix: "docker.io/myorg/",
			TargetPrefix: "reg.io/",
		},
	}

	_, err := ResolveRoute(store.ArtifactChart, "charts.helm.sh/stable/nginx", routes)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), string(ErrorRoutingNoMatch))
}

func TestResolveRoute_NonMatchingPrefix(t *testing.T) {
	routes := []*store.ClusterRoute{
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeDirect,
			SourcePrefix: "docker.io/myorg/",
			TargetPrefix: "reg.io/",
		},
	}

	_, err := ResolveRoute(store.ArtifactImage, "ghcr.io/other/app", routes)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), string(ErrorRoutingNoMatch))
}

func TestResolveRoute_PartialPrefixNoMatch(t *testing.T) {
	// Prefix "docker.io/myorg" must not match "docker.io/myorg-other/app"
	// because the next char is a hyphen, not '/' or end-of-string.
	routes := []*store.ClusterRoute{
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeDirect,
			SourcePrefix: "docker.io/myorg",
			TargetPrefix: "reg.io/",
		},
	}

	_, err := ResolveRoute(store.ArtifactImage, "docker.io/myorg-other/app", routes)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), string(ErrorRoutingNoMatch))
}

func TestResolveRoute_ExactPrefixMatch(t *testing.T) {
	routes := []*store.ClusterRoute{
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeDirect,
			SourcePrefix: "docker.io/myorg",
			TargetPrefix: "reg.io/app",
		},
	}

	result, err := ResolveRoute(store.ArtifactImage, "docker.io/myorg", routes)
	assert.NoError(t, err)
	assert.Equal(t, "reg.io/app", result.TargetURI)
}

func TestResolveRoute_PrefixWithTrailingSlash(t *testing.T) {
	routes := []*store.ClusterRoute{
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeDirect,
			SourcePrefix: "docker.io/myorg/",
			TargetPrefix: "reg.io/",
		},
	}

	// When prefix ends with "/", it matches any continuation.
	result, err := ResolveRoute(store.ArtifactImage, "docker.io/myorg/app-subdir/image", routes)
	assert.NoError(t, err)
	assert.Equal(t, "reg.io/app-subdir/image", result.TargetURI)
}

func TestResolveRoute_NilRouteSkipped(t *testing.T) {
	routes := []*store.ClusterRoute{
		nil,
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeDirect,
			SourcePrefix: "docker.io/myorg/",
			TargetPrefix: "reg.io/",
		},
	}

	result, err := ResolveRoute(store.ArtifactImage, "docker.io/myorg/app", routes)
	assert.NoError(t, err)
	assert.Equal(t, "reg.io/app", result.TargetURI)
}

func TestRoutingVersion_Deterministic(t *testing.T) {
	routes := []*store.ClusterRoute{
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeDirect,
			SourcePrefix: "docker.io/myorg/",
			TargetPrefix: "reg.io/",
		},
		{
			ArtifactType: store.ArtifactChart,
			Mode:         store.ModeReplicated,
			SourcePrefix: "charts.helm.sh/",
			TargetPrefix: "charts.io/",
		},
	}

	v1 := RoutingVersion(routes)
	assert.NotEmpty(t, v1)
	assert.Equal(t, v1, RoutingVersion(routes), "same routes must produce identical hash")

	// Different order must produce same hash.
	reversed := []*store.ClusterRoute{routes[1], routes[0]}
	assert.Equal(t, v1, RoutingVersion(reversed), "hash must be order-independent")
}

func TestRoutingVersion_ChangeDetected(t *testing.T) {
	routes := []*store.ClusterRoute{
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeDirect,
			SourcePrefix: "docker.io/myorg/",
			TargetPrefix: "reg.io/",
		},
	}

	v1 := RoutingVersion(routes)

	routes[0].TargetPrefix = "reg2.io/"
	v2 := RoutingVersion(routes)

	assert.NotEqual(t, v1, v2, "target_prefix change must produce different hash")
}

func TestRoutingVersion_NilSkipped(t *testing.T) {
	routes := []*store.ClusterRoute{
		nil,
		{
			ArtifactType: store.ArtifactImage,
			Mode:         store.ModeDirect,
			SourcePrefix: "docker.io/",
			TargetPrefix: "reg.io/",
		},
	}

	v := RoutingVersion(routes)
	assert.NotEmpty(t, v)

	vOnly := RoutingVersion(routes[1:])
	assert.Equal(t, v, vOnly)
}
