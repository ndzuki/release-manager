package preflight

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestBuildUpgradePayloadFreezesEffectiveValues(t *testing.T) {
	op := &store.Operation{
		ID:               "operation-1",
		BundleID:         "bundle-1",
		ExpectedRevision: 3,
		ValuesPatch:      []byte(`{"replicas":3}`),
	}
	definition := &store.ReleaseDefinition{
		ID:          "definition-1",
		Namespace:   "apps",
		ReleaseName: "example",
	}
	bundle := &store.ReleaseBundle{
		ID:           "bundle-1",
		DigestAlg:    "sha256",
		DigestValue:  "bundle",
		ChartRef:     "oci://registry.example.com/example",
		ChartVersion: "1.2.3",
		ChartDigest:  "sha256:chart",
		Images: []store.BundleImage{{
			Ref:        "registry.example.com/app",
			Digest:     "sha256:image",
			ValuesPath: "image",
		}},
	}
	revision := &store.ValuesRevision{
		Values:     []byte(`{"replicas":2,"image":"old"}`),
		SecretRefs: []byte(`[{"path":"database.password","name":"database","key":"password","uid":"uid-1","resource_version":"7","value_digest":"sha256:value"}]`),
	}

	payload, err := BuildUpgradePayload(op, definition, bundle, revision, "command-1", "sha256:manifest")
	require.NoError(t, err)
	assert.Equal(t, uint32(2), payload.PayloadVersion)
	upgrade := payload.Upgrade
	require.NotNil(t, upgrade)
	assert.JSONEq(t, `{"replicas":3,"image":"registry.example.com/app@sha256:image"}`, string(upgrade.GetEffectiveValuesJson()))
	assert.NotEmpty(t, upgrade.GetEffectiveValuesDigest())
	assert.Equal(t, uint64(3), upgrade.GetExpectedRevision())
	assert.True(t, upgrade.GetAtomic())
	assert.Equal(t, int32(10), upgrade.GetMaxHistory())
	assert.Equal(t, "sha256:manifest", upgrade.GetExpectedManifestDigest())
	require.Len(t, upgrade.GetSecretRefs(), 1)
	assert.Equal(t, "uid-1", upgrade.GetSecretRefs()[0].GetUid())
}

func TestBuildUpgradePayloadRejectsInvalidImagePath(t *testing.T) {
	op := &store.Operation{ID: "operation-1", BundleID: "bundle-1", ExpectedRevision: 1}
	definition := &store.ReleaseDefinition{ID: "definition-1", Namespace: "apps", ReleaseName: "example"}
	bundle := &store.ReleaseBundle{
		ID:          "bundle-1",
		Images:      []store.BundleImage{{Ref: "example/app", Digest: "sha256:image", ValuesPath: "image.repository"}},
	}
	revision := &store.ValuesRevision{Values: []byte(`{"image":"not-an-object"}`)}

	_, err := BuildUpgradePayload(op, definition, bundle, revision, "command-1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "render_failed")
}
