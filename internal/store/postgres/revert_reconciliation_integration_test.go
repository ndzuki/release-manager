//go:build integration

package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-148 / REQ-058 AC-058-33: the SQLite counterpart is
// TestSaveRevertReconciliation. Both engines must round-trip the REVERT
// reconciliation outcome, because the EmergencyResult reads it back.
func TestSaveRevertReconciliation(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	pgSeedEmergencyDefinition(t, st, "def-revert-reconcile")
	created := pgCreateEmergencyViaUOW(t, st, pgEmergencyCreateCommand(t, "def-revert-reconcile", "idem-revert", "hash-revert"))

	fresh, err := st.EmergencyIntents().GetByOperationID(ctx, created.Operation.ID)
	require.NoError(t, err)
	assert.Empty(t, fresh.RevertStatus)
	assert.Empty(t, fresh.ReconciledByOperationID)

	require.NoError(t, st.EmergencyIntents().SaveRevertReconciliation(ctx, fresh.ID, "reconciled", "op-standard-1"))

	stored, err := st.EmergencyIntents().GetByOperationID(ctx, created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, "reconciled", stored.RevertStatus)
	assert.Equal(t, "op-standard-1", stored.ReconciledByOperationID)

	assert.ErrorIs(t, st.EmergencyIntents().SaveRevertReconciliation(ctx, "missing", "reconciled", "op"), store.ErrNotFound)
}
