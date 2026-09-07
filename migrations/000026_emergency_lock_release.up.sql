-- REQ-087 / TASK-087 (D4=A, D7=A): explicit emergency target-lock release.
-- lock_released_at is an additive nullable column recording when a stuck
-- lock was released through ReleaseEmergencyLock (AUDITED_OVERRIDE keeps the
-- effect UNKNOWN while the lock is no longer held; NOT_APPLIED_PROVEN sets
-- the effect to NOT_APPLIED). Lock-holding queries and the active-lock
-- partial index exclude released intents so a released-but-UNKNOWN row no
-- longer blocks new operations while a late result may still resolve the
-- effect (REQ-032 AC-032-31).

ALTER TABLE emergency_intents
    ADD COLUMN IF NOT EXISTS lock_released_at TIMESTAMPTZ;

DROP INDEX IF EXISTS idx_ei_active_locks;
CREATE INDEX idx_ei_active_locks
    ON emergency_intents(release_definition_id, workload_kind, workload_name)
    WHERE effect_status = 'UNKNOWN' AND lock_released_at IS NULL;
