ALTER TABLE release_inventory ADD COLUMN IF NOT EXISTS observed_bundle_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN IF NOT EXISTS observed_chart_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN IF NOT EXISTS observed_effective_values_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN IF NOT EXISTS observed_manifest_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN IF NOT EXISTS live_status TEXT NOT NULL DEFAULT '';
ALTER TABLE release_inventory ADD COLUMN IF NOT EXISTS last_operation_id TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS operation_execution_results (
    operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE CASCADE,
    result_type TEXT NOT NULL,
    result_payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS rollout_trackings (
    operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending',
    resource_count INTEGER NOT NULL DEFAULT 0,
    ready_count INTEGER NOT NULL DEFAULT 0,
    failed_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
