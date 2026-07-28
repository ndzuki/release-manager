ALTER TABLE operations ADD COLUMN terminal_at TIMESTAMPTZ;

ALTER TABLE release_inventory ADD COLUMN observed_bundle_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN observed_chart_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN observed_effective_values_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN observed_manifest_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN last_operation_id TEXT NOT NULL DEFAULT '';

CREATE TABLE operation_execution_results (
    operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE CASCADE,
    result_type TEXT NOT NULL,
    result_payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE rollout_trackings (
    operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending',
    resource_count INTEGER NOT NULL DEFAULT 0,
    ready_count INTEGER NOT NULL DEFAULT 0,
    failed_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE operation_events (
    id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_operation_events_operation ON operation_events(operation_id, created_at);
