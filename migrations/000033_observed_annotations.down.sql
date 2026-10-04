-- TASK-241 rollback: drop the additive approved-annotation projection column.
-- Structural rollback only — any previously reported annotations are lost, and
-- the emergency read model returns to its fail-closed "not observed" path.
ALTER TABLE release_inventory DROP COLUMN IF EXISTS observed_annotations;
