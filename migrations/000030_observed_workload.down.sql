-- TASK-168 (REQ-058 C1/R1) rollback: drop the additive observed workload field
-- projection columns. Structural rollback only — any previously reported
-- observations are lost, and the emergency read model returns to its
-- fail-closed "not observed" path.
ALTER TABLE release_inventory DROP COLUMN IF EXISTS observed_containers;
ALTER TABLE release_inventory DROP COLUMN IF EXISTS observed_image_refs;
ALTER TABLE release_inventory DROP COLUMN IF EXISTS observed_replicas;
ALTER TABLE release_inventory DROP COLUMN IF EXISTS observed_at;
