-- REQ-085 / TASK-085 rollback: drop the additive workload identity columns.
-- Structural rollback only — any previously reported identities are lost.

ALTER TABLE release_inventory DROP COLUMN IF EXISTS workload_kind;
ALTER TABLE release_inventory DROP COLUMN IF EXISTS workload_name;
ALTER TABLE release_inventory DROP COLUMN IF EXISTS workload_namespace;
ALTER TABLE release_inventory DROP COLUMN IF EXISTS workload_uid;
