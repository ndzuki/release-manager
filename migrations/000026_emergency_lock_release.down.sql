-- REQ-087 / TASK-087 down: restore the delivery-progress-independent active
-- lock index and drop the release marker column.

DROP INDEX IF EXISTS idx_ei_active_locks;
CREATE INDEX idx_ei_active_locks
    ON emergency_intents(release_definition_id, workload_kind, workload_name)
    WHERE effect_status = 'UNKNOWN';

ALTER TABLE emergency_intents DROP COLUMN IF EXISTS lock_released_at;
