-- TASK-278 (follow-up to TASK-276 review A3): the cross-release non-terminal feed
-- (ListNonTerminalScoped) filters status NOT IN (the terminal set) and orders by
-- (created_at, id) ascending. The pre-existing operations indexes cannot serve
-- that order -- idx_operations_definition is keyed (release_definition_id, status)
-- -- so for a wide scope the planner drove from the in-scope definitions and had
-- to sort the survivors. Observed on PostgreSQL 16 with the real migrations, 30k
-- operations / 1k definitions and every customer in scope: Seq Scan on operations
-- with 29,000 rows removed by the status filter, Hash Join, then a Sort of the
-- survivors (3.0ms, 635 buffers). A narrow scope is cheap on that plan, so the
-- index must not be pinned: forced onto it, a narrow page walks the whole global
-- non-terminal index instead (SQLite, same shape: 18.2ms pinned versus 154us with
-- the planner's own choice).
--
-- This partial index keeps only non-terminal rows, in feed order, so the page
-- becomes an ordered index scan that can stop at LIMIT. Column order mirrors the
-- ORDER BY (created_at ASC, id ASC); id is required so rows sharing one
-- created_at need no secondary sort. The predicate repeats the terminal set of
-- store.OperationStatus.IsTerminal (internal/store/store.go) and of the
-- engine implementations' ListNonTerminalScoped. Neither engine pins it: with the
-- index present PostgreSQL selects it for the wide feed on its own (Index Scan,
-- no Sort; 0.11ms, 66 buffers), and SQLite learns it from the migration-time
-- PRAGMA optimize.
CREATE INDEX IF NOT EXISTS idx_operations_non_terminal_created
    ON operations(created_at, id)
    WHERE status NOT IN ('succeeded', 'failed', 'cancelled', 'timeout');
