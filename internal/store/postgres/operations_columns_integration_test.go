//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TestOperationReadsReturnEveryScannedColumn pins the class of defect where a
// query's column list drifts behind scanOperation's destination list.
//
// Three call sites (GetActiveForDefinition, List, GetByIdempotencyKey) each
// carried their own copy of a pre-emergency 16-column list while scanOperation
// had grown to 30. Any of them failed with "expected 16 destination arguments in
// Scan, not 30" -- but only when a query returned a row, so the defect stayed
// invisible until a definition happened to have a non-terminal operation, at
// which point ListReleaseInventory failed for every row.
//
// The assertion is deliberately about the *values*, not just the absence of an
// error: a list that omitted bundle_id/values_revision_id could still match a
// scan in count while silently returning empty columns.
func TestOperationReadsReturnEveryScannedColumn(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	op := &store.Operation{
		ID:                    "operation-column-parity",
		OperationType:         store.OperationInstall,
		Status:                store.StatusQueued, // non-terminal: this is the trigger
		ReleaseDefinitionID:   def.ID,
		IdempotencyKey:        "operation-column-parity-key",
		IdempotencyScope:      "org:def",
		RequestHash:           "request-hash",
		StateVersion:          1,
		BundleID:              "bundle-column-parity",
		BundleChartRef:        "oci://registry.test/chart",
		BundleChartDigest:     "sha256:chartdigest",
		PolicyVersion:         "policy-v1",
		ValuesRevisionID:      "values-revision-1",
		ExpectedRevision:      7,
		TargetRevision:        8,
		TargetOperationID:     "operation-previous",
		PatchDigest:           "sha256:patchdigest",
		EffectiveValuesDigest: "sha256:effectivedigest",
		Reason:                "replica restore",
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	// assertColumns checks every column the short list used to omit.
	assertColumns := func(t *testing.T, label string, got *store.Operation) {
		t.Helper()
		require.NotNil(t, got, "%s returned no operation", label)
		assert.Equal(t, op.ID, got.ID)
		assert.Equal(t, "bundle-column-parity", got.BundleID, "%s dropped bundle_id", label)
		assert.Equal(t, "oci://registry.test/chart", got.BundleChartRef, "%s dropped bundle_chart_ref", label)
		assert.Equal(t, "sha256:chartdigest", got.BundleChartDigest, "%s dropped bundle_chart_digest", label)
		assert.Equal(t, "policy-v1", got.PolicyVersion, "%s dropped policy_version", label)
		assert.Equal(t, "values-revision-1", got.ValuesRevisionID, "%s dropped values_revision_id", label)
		assert.Equal(t, 7, got.ExpectedRevision, "%s dropped expected_revision", label)
		assert.Equal(t, 8, got.TargetRevision, "%s dropped target_revision", label)
		assert.Equal(t, "operation-previous", got.TargetOperationID, "%s dropped target_operation_id", label)
		assert.Equal(t, "sha256:patchdigest", got.PatchDigest, "%s dropped patch_digest", label)
		assert.Equal(t, "sha256:effectivedigest", got.EffectiveValuesDigest, "%s dropped effective_values_digest", label)
		assert.Equal(t, "replica restore", got.Reason, "%s dropped reason", label)
	}

	active, err := st.Operations().GetActiveForDefinition(ctx, def.ID)
	require.NoError(t, err, "GetActiveForDefinition must not fail on a non-terminal operation")
	assertColumns(t, "GetActiveForDefinition", active)

	byKey, err := st.Operations().GetByIdempotencyKey(ctx, op.IdempotencyKey)
	require.NoError(t, err, "GetByIdempotencyKey must not fail")
	assertColumns(t, "GetByIdempotencyKey", byKey)

	listed, err := st.Operations().List(ctx, def.ID)
	require.NoError(t, err, "List must not fail")
	require.Len(t, listed, 1)
	assertColumns(t, "List", listed[0])

	// HasActiveForDefinition answers the same question the defect hit through
	// ListReleaseInventory: it must see the non-terminal operation.
	hasActive, err := st.Operations().HasActiveForDefinition(ctx, def.ID)
	require.NoError(t, err)
	assert.True(t, hasActive, "a queued operation must count as active")
}
