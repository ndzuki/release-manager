package sqlite

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TestListNonTerminalScopedPlanUsesOrderedPartialIndex is the TASK-278 AC-278-04
// regression: the shipping feed SQL must read idx_operations_non_terminal_created
// in (created_at, id) order and must not materialise a "USE TEMP B-TREE FOR
// ORDER BY".
//
// The plan is pinned with INDEXED BY on purpose. A freshly migrated SQLite
// database has no sqlite_stat1 row counts, and without them the planner prefers
// driving from release_definitions and sorting the survivors -- which is exactly
// the cost (scan proportional to operation history) this index exists to remove.
// The SQL comes from the same nonTerminalScopedSQL the store runs, so this test
// fails if the hint or the index name drifts.
func TestListNonTerminalScopedPlanUsesOrderedPartialIndex(t *testing.T) {
	st, err := Open(t.TempDir() + "/non-terminal-plan.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	now := time.Date(2026, time.October, 10, 5, 0, 0, 0, time.UTC)
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: "def-plan", Name: "plan-release", CustomerID: "cust-plan", ClusterID: "cls-001",
		Namespace: "default", ReleaseName: "plan-release", Status: store.DefStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}, nil))
	for i, status := range []store.OperationStatus{store.StatusRunning, store.StatusSucceeded} {
		require.NoError(t, st.Operations().Create(t.Context(), &store.Operation{
			ID: "op-plan-" + string(status), OperationType: store.OperationInstall, Status: status,
			ReleaseDefinitionID: "def-plan", IdempotencyKey: uuid.NewString(),
			RequestHash: "plan-hash", ExpectedRevision: 1,
			CreatedAt: now.Add(time.Duration(i) * time.Minute), UpdatedAt: now.Add(time.Duration(i) * time.Minute),
		}))
	}

	rows, err := st.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN"+nonTerminalScopedSQL(1, false), "cust-plan", 21)
	require.NoError(t, err)
	defer rows.Close()

	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		plan = append(plan, detail)
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, plan, "EXPLAIN QUERY PLAN returned no steps")

	assert.Contains(t, strings.Join(plan, "\n"), nonTerminalScopedIndex,
		"the feed query must read the ordered partial index")
	for _, line := range plan {
		assert.NotContains(t, line, "TEMP B-TREE", "the feed must not sort the page: %s", line)
	}
}
