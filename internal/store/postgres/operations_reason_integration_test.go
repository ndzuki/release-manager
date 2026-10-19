//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TASK-325 W3 double-engine evidence: operations.reason is written and read on
// PostgreSQL through the shared insertOperation/scanOperation pair, exactly as on
// SQLite. The RPC-level assertions live in
// internal/orchestrator/emergency_reason_test.go (SQLite); this test pins the
// same column on the production engine so a dialect-only regression cannot hide.
func TestOperationReasonRoundTrip(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	definition := createTestDefinition(t, st)

	const reason = "incident-42 mitigate impact"
	operation := &store.Operation{
		ID:                  uuid.NewString(),
		OperationType:       store.OperationEmergency,
		Status:              store.StatusPending,
		ReleaseDefinitionID: definition.ID,
		IdempotencyKey:      uuid.NewString(),
		RequestHash:         "reason-round-trip",
		Reason:              reason,
	}
	require.NoError(t, st.Operations().Create(ctx, operation))

	fetched, err := st.Operations().Get(ctx, operation.ID)
	require.NoError(t, err)
	assert.Equal(t, reason, fetched.Reason, "Get must read the reason column")

	listed, err := st.Operations().List(ctx, definition.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, reason, listed[0].Reason, "List must read the reason column")

	// An operation that never recorded a reason keeps the column's empty default
	// rather than inheriting a neighbour's value.
	plain := &store.Operation{
		ID:                  uuid.NewString(),
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: definition.ID,
		IdempotencyKey:      uuid.NewString(),
		RequestHash:         "reason-absent",
	}
	require.NoError(t, st.Operations().Create(ctx, plain))
	absent, err := st.Operations().Get(ctx, plain.ID)
	require.NoError(t, err)
	assert.Empty(t, absent.Reason)
}
