DROP INDEX IF EXISTS idx_ei_active_locks;
CREATE INDEX idx_ei_active_locks
    ON emergency_intents(release_definition_id, workload_kind, workload_name)
    WHERE delivery_status != 'persisted';

ALTER TABLE emergency_intents DROP COLUMN IF EXISTS effect_status;

DROP INDEX IF EXISTS idx_operation_timeline_operation;
DROP TABLE IF EXISTS operation_timeline;
