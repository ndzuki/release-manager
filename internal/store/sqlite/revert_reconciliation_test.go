package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-148 / REQ-058 AC-058-33: the REVERT reconciliation outcome must survive
// on the intent. Before this the only revert status ever produced was
// awaiting_standard_release, with nowhere to record that a later standard
// operation converged the cluster or which operation did it.
func TestSaveRevertReconciliation(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-revert-reconcile")
	created := createEmergencyViaUOW(t, st, emergencyCreateCommand(t, "def-revert-reconcile", "idem-revert", "hash-revert", store.EmergencySetReplicas))

	// A fresh intent has no reconciliation recorded.
	fresh, err := st.EmergencyIntents().GetByOperationID(ctx, created.Operation.ID)
	require.NoError(t, err)
	assert.Empty(t, fresh.RevertStatus)
	assert.Empty(t, fresh.ReconciledByOperationID)

	require.NoError(t, st.EmergencyIntents().SaveRevertReconciliation(ctx, fresh.ID, "reconciled", "op-standard-1"))

	stored, err := st.EmergencyIntents().GetByOperationID(ctx, created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, "reconciled", stored.RevertStatus)
	assert.Equal(t, "op-standard-1", stored.ReconciledByOperationID,
		"AC-058-33: the operation that converged the cluster is recorded")

	assert.ErrorIs(t, st.EmergencyIntents().SaveRevertReconciliation(ctx, "missing", "reconciled", "op"), store.ErrNotFound)
}
