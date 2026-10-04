package preflight

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-241 W-b2: the shared producer (commandPayload) that builds the INSTALL,
// ROLLBACK and preflight-stage payloads must carry the definition's annotation
// whitelist so the operator can filter inside the cluster (U1=A). Removing the
// ApprovedAnnotationKeys assignment in commandPayload makes this test fail.
func TestCommandPayloadCarriesApprovedAnnotationWhitelist(t *testing.T) {
	whitelist := []store.ApprovedAnnotationKey{
		{Key: "team", Scope: "WORKLOAD_METADATA", PromotionValuesPath: "labels.team"},
		{Key: "prometheus.io/scrape", Scope: "POD_TEMPLATE_METADATA"},
	}
	stubDefs := &stubDefinitionStore{def: &store.ReleaseDefinition{
		ID: "def-001", Namespace: "apps", ReleaseName: "example",
		ApprovedAnnotationKeys: whitelist,
	}}
	c := &Coordinator{defs: stubDefs, timeoutSeconds: 300}

	for _, opType := range []store.OperationType{
		store.OperationInstall, store.OperationRollback, store.OperationUpgrade,
	} {
		op := &store.Operation{
			ID: "op-001", OperationType: opType,
			ReleaseDefinitionID: "def-001", BundleID: "bundle-001",
		}
		payload, err := c.commandPayload(context.Background(), op, "", nil, nil)
		require.NoError(t, err)
		assert.Equalf(t, whitelist, payload.ApprovedAnnotationKeys,
			"operation type %s must carry the approved annotation whitelist", opType)

		data, err := payload.Marshal()
		require.NoError(t, err)
		decoded, err := UnmarshalCommandPayload(data)
		require.NoError(t, err)
		assert.Equalf(t, whitelist, decoded.ApprovedAnnotationKeys,
			"operation type %s whitelist lost in the JSON round-trip", opType)
	}
}

// Negative control (TASK-241 W-b2): a definition that approves no annotation
// must not put the field on the wire at all — the decoded command then carries
// no whitelist and the operator reports no annotation (fail closed). Sending
// `[]` would be indistinguishable from "observed none" only if the reader
// ignored absence; this pins the omission.
func TestCommandPayloadOmitsEmptyApprovedAnnotationWhitelist(t *testing.T) {
	stubDefs := &stubDefinitionStore{def: &store.ReleaseDefinition{
		ID: "def-001", Namespace: "apps", ReleaseName: "example",
	}}
	c := &Coordinator{defs: stubDefs, timeoutSeconds: 300}
	op := &store.Operation{
		ID: "op-001", OperationType: store.OperationUpgrade,
		ReleaseDefinitionID: "def-001", BundleID: "bundle-001",
	}

	payload, err := c.commandPayload(context.Background(), op, "", nil, nil)
	require.NoError(t, err)
	require.Empty(t, payload.ApprovedAnnotationKeys)

	data, err := payload.Marshal()
	require.NoError(t, err)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.NotContains(t, raw, "approved_annotation_keys",
		"an empty whitelist must be omitted, not serialized as an empty list")

	decoded, err := UnmarshalCommandPayload(data)
	require.NoError(t, err)
	assert.Empty(t, decoded.ApprovedAnnotationKeys)
}
