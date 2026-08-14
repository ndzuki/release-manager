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

func TestBuildUpgradePayloadRejectsInvalidImagePath(t *testing.T) {
	op := &store.Operation{ID: "operation-1", BundleID: "bundle-1", ExpectedRevision: 1}
	definition := &store.ReleaseDefinition{ID: "definition-1", Namespace: "apps", ReleaseName: "example"}
	bundle := &store.ReleaseBundle{ID: "bundle-1", Images: []store.BundleImage{{Ref: "example/app", Digest: "sha256:image", ValuesPath: "image.repository"}}}
	revision := &store.ValuesRevision{CanonicalDocument: []byte(`{"image":"not-an-object"}`)}

	_, err := BuildUpgradePayload(op, definition, bundle, revision, "command-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "render_failed")
}
