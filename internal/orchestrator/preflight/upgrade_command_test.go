package preflight

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/values"
)

func TestBuildUpgradePayloadFreezesEffectiveValues(t *testing.T) {
	op := &store.Operation{ID: "operation-1", BundleID: "bundle-1", ExpectedRevision: 3, ValuesPatch: []byte(`{"replicas":3}`)}
	definition := &store.ReleaseDefinition{ID: "definition-1", Namespace: "apps", ReleaseName: "example"}
	bundle := &store.ReleaseBundle{
		ID: "bundle-1", DigestAlg: "sha256", DigestValue: "bundle",
		ChartRef: "oci://registry.example.com/example", ChartVersion: "1.2.3", ChartDigest: "sha256:chart",
		Images: []store.BundleImage{{Ref: "registry.example.com/app", Digest: "sha256:image", ValuesPath: "image"}},
	}
	revision := &store.ValuesRevision{
		CanonicalDocument: []byte(`{"replicas":2,"image":"old"}`),
		SecretRefs:        []store.SecretRef{{Path: "database.password", Name: "database", Key: "password"}},
	}

	payload, err := BuildUpgradePayload(op, definition, bundle, revision, "command-1")
	require.NoError(t, err)
	assert.Equal(t, uint32(2), payload.PayloadVersion)
	upgrade := payload.Upgrade
	require.NotNil(t, upgrade)
	assert.JSONEq(t, `{"replicas":3,"image":"registry.example.com/app@sha256:image"}`, string(upgrade.GetEffectiveValuesJson()))
	assert.Equal(t, values.Digest(upgrade.GetEffectiveValuesJson()), upgrade.GetEffectiveValuesDigest())
	assert.Equal(t, uint64(3), upgrade.GetExpectedRevision())
	assert.True(t, upgrade.GetAtomic())
	assert.Equal(t, int32(10), upgrade.GetMaxHistory())
	require.Len(t, upgrade.GetSecretRefs(), 1)
	assert.Equal(t, "database.password", upgrade.GetSecretRefs()[0].GetPath())
	assert.Equal(t, "database", upgrade.GetSecretRefs()[0].GetName())
	assert.Equal(t, "password", upgrade.GetSecretRefs()[0].GetKey())
}

// TASK-084 AC-084-01: the wire envelope carries the top-level namespace/
// release_name so any command.GetNamespace()/GetReleaseName() consumer on the
// decoded wire Command observes the same release identity; the typed
// UpgradeCommand stays the single authoritative source for Helm execution.
func TestBuildUpgradePayloadCarriesTopLevelIdentity(t *testing.T) {
	op := &store.Operation{ID: "operation-1", BundleID: "bundle-1", ExpectedRevision: 1}
	definition := &store.ReleaseDefinition{ID: "definition-1", Namespace: "apps", ReleaseName: "example"}
	bundle := &store.ReleaseBundle{
		ID: "bundle-1", DigestAlg: "sha256", DigestValue: "bundle",
		ChartRef: "oci://registry.example.com/example", ChartVersion: "1.2.3", ChartDigest: "sha256:chart",
	}
	revision := &store.ValuesRevision{CanonicalDocument: []byte(`{"replicas":1}`)}

	payload, err := BuildUpgradePayload(op, definition, bundle, revision, "command-1")
	require.NoError(t, err)
	assert.Equal(t, "apps", payload.Namespace)
	assert.Equal(t, "example", payload.ReleaseName)
	assert.Equal(t, payload.Namespace, payload.Upgrade.GetNamespace())
	assert.Equal(t, payload.ReleaseName, payload.Upgrade.GetReleaseName())
}

func TestBuildUpgradePayloadRejectsInvalidImagePath(t *testing.T) {
	op := &store.Operation{ID: "operation-1", BundleID: "bundle-1", ExpectedRevision: 1}
	definition := &store.ReleaseDefinition{ID: "definition-1", Namespace: "apps", ReleaseName: "example"}
	bundle := &store.ReleaseBundle{ID: "bundle-1", Images: []store.BundleImage{{Ref: "example/app", Digest: "sha256:image", ValuesPath: "image.repository"}}}
	revision := &store.ValuesRevision{CanonicalDocument: []byte(`{"image":"not-an-object"}`)}

	_, err := BuildUpgradePayload(op, definition, bundle, revision, "command-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "render_failed")
}

// AC-082-01 (D-108 ①a): mergeEffectiveValues applies bundle image overrides
// with the same tolerant merge semantics the operator uses when it renders
// (internal/operator/agent/agent.go applyBundleImageOverrides): missing
// intermediate objects are created on demand, so approved values without an
// image object (the dev fixture ships `{"replicaCount":1}`) no longer fail
// with render_failed. Only real traversal errors stay fatal: an existing
// non-object intermediate segment, a leaf that cannot accept a string
// (AC-021-13), and an empty path.
func TestMergeEffectiveValuesImageOverrideTolerance(t *testing.T) {
	image := store.BundleImage{Ref: "registry.example.com/app", Digest: "sha256:digest", ValuesPath: "image.repository"}

	tests := []struct {
		name      string
		base      string
		images    []store.BundleImage
		want      string
		wantError string
	}{
		{
			name:   "missing intermediate object is created",
			base:   `{"replicaCount":1}`,
			images: []store.BundleImage{image},
			want:   `{"replicaCount":1,"image":{"repository":"registry.example.com/app@sha256:digest"}}`,
		},
		{
			name:   "existing intermediate object is preserved and overridden",
			base:   `{"image":{"repository":"old","tag":"v1"}}`,
			images: []store.BundleImage{image},
			want:   `{"image":{"repository":"registry.example.com/app@sha256:digest","tag":"v1"}}`,
		},
		{
			name:      "existing non-object intermediate segment stays fatal",
			base:      `{"image":"not-an-object"}`,
			images:    []store.BundleImage{image},
			wantError: `values_path "image.repository" does not reference an object`,
		},
		{
			name:      "leaf that cannot accept a string stays fatal",
			base:      `{"image":{"repository":123}}`,
			images:    []store.BundleImage{image},
			wantError: `values_path "image.repository" does not accept a string`,
		},
		{
			name:      "empty path stays fatal",
			base:      `{"replicaCount":1}`,
			images:    []store.BundleImage{{Ref: "registry.example.com/app", Digest: "sha256:digest", ValuesPath: ""}},
			wantError: "image values_path is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective, err := mergeEffectiveValues([]byte(tt.base), nil, tt.images)
			if tt.wantError != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, "render_failed")
				assert.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(effective))
		})
	}
}

// AC-082-01 (D-108 ①a): the frozen upgrade payload must carry the tolerantly
// merged effective values end to end, so the operator's digest check
// (agent.go EffectiveValuesDigest) sees exactly what the merge produced.
func TestBuildUpgradeCommandToleratesMissingImageObject(t *testing.T) {
	op := &store.Operation{ID: "operation-1", BundleID: "bundle-1", ExpectedRevision: 1}
	definition := &store.ReleaseDefinition{ID: "definition-1", Namespace: "apps", ReleaseName: "example"}
	bundle := &store.ReleaseBundle{
		ID: "bundle-1", ChartRef: "oci://registry.example.com/example", ChartVersion: "1.2.3", ChartDigest: "sha256:chart",
		Images: []store.BundleImage{{Ref: "registry.example.com/app", Digest: "sha256:digest", ValuesPath: "image.repository"}},
	}
	revision := &store.ValuesRevision{CanonicalDocument: []byte(`{"replicaCount":1}`)}

	upgrade, err := BuildUpgradeCommand(op, definition, bundle, revision, "command-1")
	require.NoError(t, err)
	assert.JSONEq(t, `{"replicaCount":1,"image":{"repository":"registry.example.com/app@sha256:digest"}}`, string(upgrade.GetEffectiveValuesJson()))
	assert.Equal(t, values.Digest(upgrade.GetEffectiveValuesJson()), upgrade.GetEffectiveValuesDigest())
}
