-- TASK-278 (follow-up to TASK-276 review A3): the cross-release non-terminal feed
-- (ListNonTerminalScoped) filters status NOT IN (the terminal set) and orders by
-- (created_at, id) ascending. The pre-existing operations indexes cannot serve
-- that order -- idx_operations_definition is keyed (release_definition_id, status)
-- -- so the planner scanned every historical operation of the in-scope
-- definitions and sorted the survivors (observed: SEARCH operations USING INDEX
-- idx_operations_definition + "USE TEMP B-TREE FOR ORDER BY", cost growing with
-- operation history even though the page is LIMIT 101).
--
-- This partial index keeps only non-terminal rows, in feed order, so the page
-- becomes an ordered index scan that can stop at LIMIT. Column order mirrors the
-- ORDER BY (created_at ASC, id ASC); id is required so rows sharing one
-- created_at need no secondary sort. The predicate repeats the terminal set of
-- store.OperationStatus.IsTerminal (internal/store/store.go) and of the
-- engine implementations' ListNonTerminalScoped.
CREATE INDEX IF NOT EXISTS idx_operations_non_terminal_created
    ON operations(created_at, id)
    WHERE status NOT IN ('succeeded', 'failed', 'cancelled', 'timeout');
