package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestValidateRouteConfig(t *testing.T) {
	tests := []struct {
		name         string
		artifactType store.ArtifactType
		mode         store.ArtifactMode
		sourcePrefix string
		targetPrefix string
		wantErr      bool
		errContains  string
	}{
		{
			name:         "valid image direct route",
			artifactType: store.ArtifactImage,
			mode:         store.ModeDirect,
			sourcePrefix: "docker.io/myorg",
			targetPrefix: "registry.internal/myorg",
		},
		{
			name:         "valid image pull_through_cache route",
			artifactType: store.ArtifactImage,
			mode:         store.ModePullThroughCache,
			sourcePrefix: "docker.io/library",
			targetPrefix: "cache.internal/library",
		},
		{
			name:         "valid image replicated route",
			artifactType: store.ArtifactImage,
			mode:         store.ModeReplicated,
			sourcePrefix: "quay.io/app",
			targetPrefix: "replica.internal/app",
		},
		{
			name:         "valid chart direct route",
			artifactType: store.ArtifactChart,
			mode:         store.ModeDirect,
			sourcePrefix: "charts.helm.sh/stable",
			targetPrefix: "registry.internal/charts",
		},
		{
			name:         "valid chart replicated route",
			artifactType: store.ArtifactChart,
			mode:         store.ModeReplicated,
			sourcePrefix: "charts.example.com",
			targetPrefix: "replica.internal/charts",
		},
		{
			name:         "chart pull_through_cache not supported",
			artifactType: store.ArtifactChart,
			mode:         store.ModePullThroughCache,
			sourcePrefix: "charts.helm.sh",
			targetPrefix: "cache.internal",
			wantErr:      true,
			errContains:  "not supported",
		},
		{
			name:         "invalid artifact type",
			artifactType: store.ArtifactType("invalid"),
			mode:         store.ModeDirect,
			sourcePrefix: "src",
			targetPrefix: "dst",
			wantErr:      true,
			errContains:  "invalid artifact_type",
		},
		{
			name:         "invalid mode",
			artifactType: store.ArtifactImage,
			mode:         store.ArtifactMode("invalid"),
			sourcePrefix: "src",
			targetPrefix: "dst",
			wantErr:      true,
			errContains:  "invalid mode",
		},
		{
			name:         "empty source_prefix",
			artifactType: store.ArtifactImage,
			mode:         store.ModeDirect,
			sourcePrefix: "",
			targetPrefix: "dst",
			wantErr:      true,
			errContains:  "source_prefix must not be empty",
		},
		{
			name:         "empty target_prefix",
			artifactType: store.ArtifactImage,
			mode:         store.ModeDirect,
			sourcePrefix: "src",
			targetPrefix: "",
			wantErr:      true,
			errContains:  "target_prefix must not be empty",
		},
		{
			name:         "whitespace in source_prefix",
			artifactType: store.ArtifactImage,
			mode:         store.ModeDirect,
			sourcePrefix: "src with space",
			targetPrefix: "dst",
			wantErr:      true,
			errContains:  "source_prefix must not contain whitespace",
		},
		{
			name:         "whitespace in target_prefix",
			artifactType: store.ArtifactImage,
			mode:         store.ModeDirect,
			sourcePrefix: "src",
			targetPrefix: "dst\twith\ttab",
			wantErr:      true,
			errContains:  "target_prefix must not contain whitespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRouteConfig(tt.artifactType, tt.mode, tt.sourcePrefix, tt.targetPrefix)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDetectConflictingRoutes(t *testing.T) {
	tests := []struct {
		name     string
		existing []*store.ClusterRoute
		newRoute *store.ClusterRoute
		wantErr  bool
	}{
		{
			name:     "no conflict with empty list",
			existing: []*store.ClusterRoute{},
			newRoute: &store.ClusterRoute{ID: "new", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/myorg"},
		},
		{
			name: "no conflict with different artifact type",
			existing: []*store.ClusterRoute{
				{ID: "r1", ClusterID: "c1", ArtifactType: store.ArtifactChart, SourcePrefix: "docker.io/myorg"},
			},
			newRoute: &store.ClusterRoute{ID: "new", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/myorg"},
		},
		{
			name: "no conflict with different prefix",
			existing: []*store.ClusterRoute{
				{ID: "r1", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/other"},
			},
			newRoute: &store.ClusterRoute{ID: "new", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/myorg"},
		},
		{
			name: "conflict: same cluster, type, and prefix",
			existing: []*store.ClusterRoute{
				{ID: "r1", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/myorg"},
			},
			newRoute: &store.ClusterRoute{ID: "new", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/myorg"},
			wantErr:  true,
		},
		{
			name: "no conflict: updating same route",
			existing: []*store.ClusterRoute{
				{ID: "r1", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/myorg"},
			},
			newRoute: &store.ClusterRoute{ID: "r1", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/myorg"},
		},
		{
			name: "conflict with multiple existing routes",
			existing: []*store.ClusterRoute{
				{ID: "r1", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/a"},
				{ID: "r2", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/b"},
				{ID: "r3", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/myorg"},
			},
			newRoute: &store.ClusterRoute{ID: "new", ClusterID: "c1", ArtifactType: store.ArtifactImage, SourcePrefix: "docker.io/myorg"},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := DetectConflictingRoutes(tt.existing, tt.newRoute)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
