package operator_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/orchestrator/preflight"
	"github.com/ndzuki/release-manager/internal/store"
)

func upgradeWhitelistPayload(t *testing.T, whitelist []store.ApprovedAnnotationKey) []byte {
	t.Helper()
	definition := &store.ReleaseDefinition{
		ID: "def-1", Namespace: "apps", ReleaseName: "example",
		ApprovedAnnotationKeys: whitelist,
	}
	op := &store.Operation{
		ID: "op-1", BundleID: "bundle-1", OperationType: store.OperationUpgrade,
		ExpectedRevision: 4,
	}
	bundle := &store.ReleaseBundle{
		ID: "bundle-1", DigestAlg: "sha256", DigestValue: "deadbeef",
		ChartRef: "oci://example/chart", ChartVersion: "1.2.3", ChartDigest: "sha256:chart",
	}
	revision := &store.ValuesRevision{ID: "rev-1", CanonicalDocument: []byte(`{"replicaCount":1}`)}

	payload, err := preflight.BuildUpgradePayload(op, definition, bundle, revision, "op-1:execute")
	require.NoError(t, err)
	encoded, err := payload.Marshal()
	require.NoError(t, err)
	return encoded
}

func decodeWhitelist(t *testing.T, encoded []byte) map[string]store.ApprovedAnnotationKey {
	t.Helper()
	var command operatorv1.Command
	require.NoError(t, operator.DecodeCommandPayload(encoded, &command))
	got := make(map[string]store.ApprovedAnnotationKey, len(command.GetApprovedAnnotationKeys()))
	for _, entry := range command.GetApprovedAnnotationKeys() {
		got[entry.GetScope()+"\x00"+entry.GetKey()] = store.ApprovedAnnotationKey{
			Key: entry.GetKey(), Scope: entry.GetScope(), PromotionValuesPath: entry.GetPromotionValuesPath(),
		}
	}
	return got
}

func whitelistSet(keys ...store.ApprovedAnnotationKey) map[string]store.ApprovedAnnotationKey {
	out := make(map[string]store.ApprovedAnnotationKey, len(keys))
	for _, key := range keys {
		out[key.Scope+"\x00"+key.Key] = key
	}
	return out
}

// TASK-241 W-b2 round trip: the UPGRADE producer freezes the definition's
// two-scope annotation whitelist into the outbox payload, and the operator's
// decoder reconstitutes exactly that set on the wire Command. Removing the
// ApprovedAnnotationKeys assignment in BuildUpgradePayload makes this test red.
func TestDecodeCommandPayloadAnnotationWhitelistRoundTrip(t *testing.T) {
	encoded := upgradeWhitelistPayload(t, []store.ApprovedAnnotationKey{
		{Key: "team", Scope: "WORKLOAD_METADATA", PromotionValuesPath: "labels.team"},
		{Key: "prometheus.io/scrape", Scope: "POD_TEMPLATE_METADATA"},
	})
	assert.Equal(t, whitelistSet(
		store.ApprovedAnnotationKey{Key: "team", Scope: "WORKLOAD_METADATA", PromotionValuesPath: "labels.team"},
		store.ApprovedAnnotationKey{Key: "prometheus.io/scrape", Scope: "POD_TEMPLATE_METADATA"},
	), decodeWhitelist(t, encoded))

	// Order independence: the same entries in the other order decode to the
	// same set, so a consumer must not depend on the producer's list order.
	reordered := upgradeWhitelistPayload(t, []store.ApprovedAnnotationKey{
		{Key: "prometheus.io/scrape", Scope: "POD_TEMPLATE_METADATA"},
		{Key: "team", Scope: "WORKLOAD_METADATA", PromotionValuesPath: "labels.team"},
	})
	assert.Equal(t, decodeWhitelist(t, encoded), decodeWhitelist(t, reordered))
}

// Negative control (TASK-241 W-b2): a definition that approves no annotation
// must leave the field off the wire, so the decoded Command carries no
// whitelist and the agent's rememberApprovedAnnotations clears its cache —
// no annotation value is ever reported (fail closed). The agent-side half is
// pinned by TestAgent_AnnotationObservationReleaseWriteWithoutWhitelistClearsCache
// and TestAgent_AnnotationObservationWithoutCommandReportsNothing.
func TestDecodeCommandPayloadOmitsEmptyAnnotationWhitelist(t *testing.T) {
	encoded := upgradeWhitelistPayload(t, nil)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &raw))
	assert.NotContains(t, raw, "approved_annotation_keys",
		"an empty whitelist must be omitted from the payload")

	var command operatorv1.Command
	require.NoError(t, operator.DecodeCommandPayload(encoded, &command))
	assert.Empty(t, command.GetApprovedAnnotationKeys())
}
