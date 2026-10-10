//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
)

// TestListNonTerminalScopedPagesAndScopes is the PostgreSQL half of TASK-276
// AC-276-02: the scoped, keyset-paginated non-terminal feed must behave like the
// SQLite implementation (oldest first on (created_at, id), closed customer scope,
// terminal rows excluded). It runs only with POSTGRES_TEST_DSN, so the SQLite
// orchestrator tests remain the always-on coverage.
func TestListNonTerminalScopedPagesAndScopes(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	inScope := uuid.New().String()
	defA := createDefinitionForCustomer(t, st, inScope, "release-a")
	defB := createDefinitionForCustomer(t, st, inScope, "release-b")
	defC := createDefinitionForCustomer(t, st, inScope, "release-c")
	defOutside := createTestDefinition(t, st) // another customer, out of scope

	base := time.Date(2026, time.October, 10, 3, 0, 0, 0, time.UTC)
	seedScopedOperation(t, st, "op-a", defA.ID, store.OperationInstall, store.StatusPending, base)
	seedScopedOperation(t, st, "op-b", defB.ID, store.OperationUpgrade, store.StatusQueued, base.Add(time.Minute))
	// op-b and op-c share created_at: the id tiebreaker is what keeps the keyset
	// cursor from skipping or repeating a row across the page boundary.
	seedScopedOperation(t, st, "op-c", defC.ID, store.OperationRollback, store.StatusRunning, base.Add(time.Minute))
	seedScopedOperation(t, st, "op-d", defA.ID, store.OperationEmergency, store.StatusCancelling, base.Add(2*time.Minute))
	seedScopedOperation(t, st, "op-terminal", defA.ID, store.OperationInstall, store.StatusSucceeded, base.Add(-time.Minute))
	seedScopedOperation(t, st, "op-outside", defOutside.ID, store.OperationInstall, store.StatusRunning, base)

	first, err := st.Operations().ListNonTerminalScoped(ctx, store.NonTerminalOperationQuery{
		CustomerIDs: []string{inScope}, PageSize: 2,
	})
	require.NoError(t, err)
	require.Len(t, first.Rows, 2)
	assert.True(t, first.HasMore)
	assert.Equal(t, "op-a", first.Rows[0].Operation.ID)
	assert.Equal(t, "release-a", first.Rows[0].DefinitionName)
	assert.Equal(t, inScope, first.Rows[0].CustomerID)
	assert.Equal(t, "op-b", first.Rows[1].Operation.ID)

	last := first.Rows[len(first.Rows)-1]
	second, err := st.Operations().ListNonTerminalScoped(ctx, store.NonTerminalOperationQuery{
		CustomerIDs: []string{inScope}, PageSize: 2,
		HasCursor: true, CursorTime: last.Operation.CreatedAt, CursorID: last.Operation.ID,
	})
	require.NoError(t, err)
	require.Len(t, second.Rows, 2)
	assert.False(t, second.HasMore, "the second page is the last one")
	assert.Equal(t, []string{"op-c", "op-d"}, []string{second.Rows[0].Operation.ID, second.Rows[1].Operation.ID})
	assert.Equal(t, store.OperationEmergency, second.Rows[1].Operation.OperationType)

	for _, row := range append(first.Rows, second.Rows...) {
		assert.NotEqual(t, "op-terminal", row.Operation.ID, "terminal operations are excluded")
		assert.NotEqual(t, "op-outside", row.Operation.ID, "another customer's operation leaked into the scope")
	}
}

// TestListNonTerminalScopedEmptyScopeReturnsNoRows pins the store contract that
// an empty CustomerIDs is "nothing visible", never "everything".
func TestListNonTerminalScopedEmptyScopeReturnsNoRows(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	seedScopedOperation(t, st, "op-empty-scope", def.ID, store.OperationInstall, store.StatusRunning, time.Now().UTC())

	page, err := st.Operations().ListNonTerminalScoped(ctx, store.NonTerminalOperationQuery{PageSize: 20})
	require.NoError(t, err)
	assert.Empty(t, page.Rows)
	assert.False(t, page.HasMore)
}

func createDefinitionForCustomer(
	t *testing.T,
	st *postgresstore.Store,
	customerID, name string,
) *store.ReleaseDefinition {
	t.Helper()
	def := &store.ReleaseDefinition{
		ID: uuid.New().String(), Name: name, CustomerID: customerID,
		ClusterID: uuid.New().String(), Status: store.DefStatusDraft,
	}
	require.NoError(t, st.Definitions().Create(context.Background(), def, nil))
	return def
}

func seedScopedOperation(
	t *testing.T,
	st *postgresstore.Store,
	id, definitionID string,
	opType store.OperationType,
	status store.OperationStatus,
	createdAt time.Time,
) {
	t.Helper()
	require.NoError(t, st.Operations().Create(context.Background(), &store.Operation{
		ID: id, OperationType: opType, Status: status,
		ReleaseDefinitionID: definitionID, IdempotencyKey: id + "-key",
		RequestHash: id + "-hash", ExpectedRevision: 1,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}))
}
