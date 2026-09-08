-- REQ-088 / TASK-088 rollback: drop the orchestrator-side pending identity
-- buffer. Structural rollback only — any still-pending (unbound) identities
-- are lost; already-bound identities live on the release_inventory columns
-- (migration 000024) and are unaffected.
DROP TABLE IF EXISTS pending_workload_identity;
