-- TASK-278 rollback: drop the ordered partial index backing the cross-release
-- non-terminal feed. Structural rollback only; the feed keeps working through the
-- old (scan + sort) plan, just more slowly as operation history grows.
DROP INDEX IF EXISTS idx_operations_non_terminal_created;
