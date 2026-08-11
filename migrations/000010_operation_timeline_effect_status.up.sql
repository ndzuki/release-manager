CREATE TABLE IF NOT EXISTS operation_timeline (
    id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    sequence BIGINT NOT NULL,
    entry_type TEXT NOT NULL,
    state_version BIGINT NOT NULL,
    data JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(operation_id, sequence)
);
CREATE INDEX IF NOT EXISTS idx_operation_timeline_operation ON operation_timeline(operation_id, sequence);

ALTER TABLE emergency_intents
    ADD COLUMN IF NOT EXISTS effect_status TEXT NOT NULL DEFAULT 'UNKNOWN'
        CHECK (effect_status IN ('UNKNOWN', 'APPLIED', 'NOT_APPLIED'));

DROP INDEX IF EXISTS idx_ei_active_locks;
CREATE INDEX idx_ei_active_locks
    ON emergency_intents(release_definition_id, workload_kind, workload_name)
    WHERE effect_status = 'UNKNOWN';
