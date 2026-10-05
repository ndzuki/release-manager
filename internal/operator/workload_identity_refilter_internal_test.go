package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ndzuki/release-manager/internal/store"
)

// TestFilterApprovedAnnotations pins the center-side re-filter seam
// (TASK-247) across the three ingest semantics and the fail-closed edges:
//
//	(a) a mixed report keeps only the approved (scope, key) pairs;
//	(b) a report whose keys are all unapproved collapses to nil, which the
//	    store writes as the "not observed" sentinel — the stale key is dropped;
//	(c) a report with no annotations at all stays nil (nothing is fabricated),
//	    preserving the pre-existing empty-projection semantics.
//
// It also pins that a nil definition approves nothing (unknown whitelist is
// fail closed) and that matching is exact on (scope, key) after trimming.
func TestFilterApprovedAnnotations(t *testing.T) {
	definition := func(keys ...store.ApprovedAnnotationKey) *store.ReleaseDefinition {
		return &store.ReleaseDefinition{ApprovedAnnotationKeys: keys}
	}
	workloadTeam := store.ApprovedAnnotationKey{Key: "team", Scope: AnnotationScopeWorkloadMetadata}
	podScrape := store.ApprovedAnnotationKey{Key: "prometheus.io/scrape", Scope: AnnotationScopePodTemplateMetadata}

	tests := []struct {
		name       string
		definition *store.ReleaseDefinition
		reported   map[string]map[string]string
		want       map[string]map[string]string
	}{
		{
			name:       "(a) mixed report keeps the approved subset only",
			definition: definition(workloadTeam),
			reported: map[string]map[string]string{
				AnnotationScopeWorkloadMetadata: {"team": "platform", "removed": "stale"},
				AnnotationScopePodTemplateMetadata: {
					"prometheus.io/scrape": "true",
				},
			},
			want: map[string]map[string]string{AnnotationScopeWorkloadMetadata: {"team": "platform"}},
		},
		{
			name:       "(a) approved key survives together with an unapproved scope",
			definition: definition(workloadTeam, podScrape),
			reported: map[string]map[string]string{
				AnnotationScopeWorkloadMetadata: {"team": "platform"},
				"UNKNOWN_SCOPE":                 {"team": "wrong-scope"},
			},
			want: map[string]map[string]string{AnnotationScopeWorkloadMetadata: {"team": "platform"}},
		},
		{
			name:       "(b) all reported keys unapproved collapses to not-observed",
			definition: definition(workloadTeam),
			reported: map[string]map[string]string{
				AnnotationScopeWorkloadMetadata: {"legacy": "stale"},
			},
			want: nil,
		},
		{
			name:       "(b) empty definition whitelist approves nothing",
			definition: definition(),
			reported: map[string]map[string]string{
				AnnotationScopeWorkloadMetadata: {"team": "platform"},
			},
			want: nil,
		},
		{
			name:       "(c) no annotations reported stays nil",
			definition: definition(workloadTeam),
			reported:   nil,
			want:       nil,
		},
		{
			name:       "(c) empty report stays nil",
			definition: definition(workloadTeam),
			reported:   map[string]map[string]string{},
			want:       nil,
		},
		{
			name:       "nil definition approves nothing (fail closed)",
			definition: nil,
			reported: map[string]map[string]string{
				AnnotationScopeWorkloadMetadata: {"team": "platform"},
			},
			want: nil,
		},
		{
			name:       "scope must match: same key in the wrong scope is dropped",
			definition: definition(podScrape),
			reported: map[string]map[string]string{
				AnnotationScopeWorkloadMetadata: {"prometheus.io/scrape": "true"},
			},
			want: nil,
		},
		{
			name:       "blank approved entries never match",
			definition: definition(store.ApprovedAnnotationKey{Key: " ", Scope: AnnotationScopeWorkloadMetadata}, store.ApprovedAnnotationKey{Key: "team", Scope: " "}),
			reported: map[string]map[string]string{
				AnnotationScopeWorkloadMetadata: {"team": "platform"},
			},
			want: nil,
		},
		{
			name:       "matching trims scope and key on both sides",
			definition: definition(store.ApprovedAnnotationKey{Key: " team ", Scope: " " + AnnotationScopeWorkloadMetadata + " "}),
			reported: map[string]map[string]string{
				" " + AnnotationScopeWorkloadMetadata + " ": {" team ": "platform"},
			},
			want: map[string]map[string]string{AnnotationScopeWorkloadMetadata: {"team": "platform"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, filterApprovedAnnotations(tt.definition, tt.reported))
		})
	}
}
