package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// feedPlan* describe the fixture the plan regression below is measured against:
// six customers, sixty definitions, and a 2,000-operation backlog whose
// non-terminal rows are an order of magnitude more numerous than one narrow
// customer's.
const (
	feedPlanCustomers = 6
	feedPlanPageSize  = 21 // one page of 20 plus the next-page probe
)

// seedFeedPlanFixture loads the plan fixture into a migrated store. Customer c1
// owns definitions 1..10; c2..c6 own the 2,000-operation backlog, one operation
// in ten non-terminal. c1's ten non-terminal operations carry the NEWEST
// created_at values, so a narrow c1 page sits at the far end of the ordered
// non-terminal index -- the shape where pinning the index costs a full walk.
func seedFeedPlanFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `WITH RECURSIVE seq(k) AS (SELECT 1 UNION ALL SELECT k+1 FROM seq WHERE k < 6)
		INSERT INTO customers (id, name, slug, created_at, updated_at)
		SELECT 'c'||k, 'n'||k, 's'||k, '', '' FROM seq`)
	mustExec(t, db, `WITH RECURSIVE seq(k) AS (SELECT 1 UNION ALL SELECT k+1 FROM seq WHERE k < 60)
		INSERT INTO release_definitions (id, name, customer_id, cluster_id, namespace, release_name, status, created_at, updated_at)
		SELECT 'def-'||k, 'n', 'c'||(1+((k-1)/10)), 'cls-001', 'default', 'r'||k, 'active', '', '' FROM seq`)
	mustExec(t, db, `WITH RECURSIVE seq(k) AS (SELECT 1 UNION ALL SELECT k+1 FROM seq WHERE k < 2000)
		INSERT INTO operations (id, operation_type, status, release_definition_id, idempotency_key, request_hash, expected_revision, created_at, updated_at)
		SELECT 'op-'||k, 'install', CASE WHEN k%10=0 THEN 'running' ELSE 'succeeded' END,
			'def-'||(11+(k%50)), 'k'||k, 'h', 1, printf('%010d', k), printf('%010d', k) FROM seq`)
	mustExec(t, db, `WITH RECURSIVE seq(k) AS (SELECT 1 UNION ALL SELECT k+1 FROM seq WHERE k < 30)
		INSERT INTO operations (id, operation_type, status, release_definition_id, idempotency_key, request_hash, expected_revision, created_at, updated_at)
		SELECT 'tail-'||k, 'install', CASE WHEN k%3=0 THEN 'running' ELSE 'succeeded' END,
			'def-'||(1+((k-1)/3)), 'tail'||k, 'h', 1, printf('%010d', 3000+k), printf('%010d', 3000+k) FROM seq`)
}

// analyzeFeedPlanFixture rebuilds planner statistics after the fixture is
// loaded. It is explicit because the store is created empty and nothing else
// would leave statistics behind: Open takes the fresh path for a new database,
// and migrateFresh does not run PRAGMA optimize (see optimizePlannerStatistics
// in db.go), so without this ANALYZE the query below would have no sqlite_stat1
// to plan against. The deliberate ANALYZE keeps this test independent of where
// the statistics happen to come from, so it still discriminates a missing index
// or a wrong column order (see TestMigrateBuildsPlannerStatisticsForTheFeedIndex
// for the migration half).
func analyzeFeedPlanFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `ANALYZE`)
}

// TestListNonTerminalScopedPlanUsesOrderedPartialIndex is the TASK-278 AC-278-04
// regression: the shipping feed SQL must read idx_operations_non_terminal_created
// in (created_at, id) order and must not materialise a "USE TEMP B-TREE FOR
// ORDER BY".
//
// The plan is no longer pinned with INDEXED BY. A wide scope -- the whole
// backlog in view -- is where the ordered partial index wins, and with the
// statistics the migration now builds the planner takes it on its own. Without
// the index, or with the key columns swapped to (id, created_at), this test
// fails; see TestNonTerminalScopedSQLDoesNotPinTheIndex for the companion guard
// that the pin is not reintroduced (which would make narrow scopes pay a full
// index walk).
func TestListNonTerminalScopedPlanUsesOrderedPartialIndex(t *testing.T) {
	st, err := Open(t.TempDir() + "/non-terminal-plan.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	seedFeedPlanFixture(t, st.DB())
	analyzeFeedPlanFixture(t, st.DB())

	wideArgs := make([]any, 0, feedPlanCustomers+1)
	for i := 1; i <= feedPlanCustomers; i++ {
		wideArgs = append(wideArgs, fmt.Sprintf("c%d", i))
	}
	wideArgs = append(wideArgs, feedPlanPageSize)

	plan := explainQueryPlan(t, st.DB(), nonTerminalScopedSQL(feedPlanCustomers, false), wideArgs...)
	joined := strings.Join(plan, "\n")
	require.NotEmpty(t, plan, "EXPLAIN QUERY PLAN returned no steps")

	assert.Contains(t, joined, nonTerminalScopedIndex,
		"the wide feed must read the ordered partial index; plan:\n%s", joined)
	assert.NotContains(t, joined, "TEMP B-TREE",
		"the wide feed must not sort the page; plan:\n%s", joined)
}

// TestNonTerminalScopedSQLDoesNotPinTheIndex is the review follow-up guard: the
// pin (INDEXED BY) was removed because it ignored scope selectivity, forcing a
// full walk of the global non-terminal index for narrow scopes (measured at 90k
// operations with a ten-row customer scope: ~160us planner-chosen versus ~18ms
// pinned). The SQL must stay free of the hint so the planner can keep the
// definition-driven plan for a narrow scope and take the partial index for a
// wide one.
func TestNonTerminalScopedSQLDoesNotPinTheIndex(t *testing.T) {
	assert.NotContains(t, nonTerminalScopedSQL(1, false), "INDEXED BY")
	assert.NotContains(t, nonTerminalScopedSQL(feedPlanCustomers, true), "INDEXED BY")
}

// TestMigrateBuildsPlannerStatisticsForTheFeedIndex is the condition (a)
// regression: opening an existing, populated database must leave the planner
// statistics that the unpinned feed plan depends on. A fresh schema template is
// deliberately not analyzed (ANALYZE there would write a degenerate "0 0 0"
// sqlite_stat1 row for the partial index that later pins both scopes to it), so
// the statistics have to arrive from the migration of a database that already
// carries operations.
func TestMigrateBuildsPlannerStatisticsForTheFeedIndex(t *testing.T) {
	ctx := t.Context()
	path := t.TempDir() + "/non-terminal-stats.db"
	st, err := Open(path)
	require.NoError(t, err)
	seedFeedPlanFixture(t, st.DB())
	require.NoError(t, st.Close())

	// Reopening a non-empty database runs migrateLegacy, whose post-commit
	// PRAGMA optimize is the only thing that can create sqlite_stat1 here.
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	var stat string
	require.NoError(t, reopened.DB().QueryRowContext(ctx,
		`SELECT stat FROM sqlite_stat1 WHERE tbl = 'operations' AND idx = ?`,
		nonTerminalScopedIndex).Scan(&stat),
		"the migration must leave sqlite_stat1 rows for the feed index")
	assert.NotEqual(t, "0 0 0", stat,
		"the statistics must describe the loaded rows, not the empty database ANALYZE would have seen")

	wideArgs := make([]any, 0, feedPlanCustomers+1)
	for i := 1; i <= feedPlanCustomers; i++ {
		wideArgs = append(wideArgs, fmt.Sprintf("c%d", i))
	}
	wideArgs = append(wideArgs, feedPlanPageSize)
	plan := explainQueryPlan(t, reopened.DB(), nonTerminalScopedSQL(feedPlanCustomers, false), wideArgs...)
	joined := strings.Join(plan, "\n")
	assert.Contains(t, joined, nonTerminalScopedIndex,
		"the migration statistics must let the wide feed choose the index; plan:\n%s", joined)
	assert.NotContains(t, joined, "TEMP B-TREE", "plan:\n%s", joined)
}

// explainQueryPlan returns the detail column of EXPLAIN QUERY PLAN for sql.
func explainQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN"+query, args...)
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
	return plan
}

// mustExec runs a fixture statement, failing the test on error.
func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), query)
	require.NoError(t, err)
}
