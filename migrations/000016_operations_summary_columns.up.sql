-- TASK-067 v5: operations digest/scope parity with the sqlite port.
-- The PostgreSQL operation creation UOW persists the fixed summary fields
-- (AC-067-19); the baseline table predates them.
ALTER TABLE operations ADD COLUMN IF NOT EXISTS idempotency_scope TEXT NOT NULL DEFAULT '';
ALTER TABLE operations ADD COLUMN IF NOT EXISTS bundle_chart_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE operations ADD COLUMN IF NOT EXISTS bundle_chart_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE operations ADD COLUMN IF NOT EXISTS image_refs_json JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE operations ADD COLUMN IF NOT EXISTS image_digests_json JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE operations ADD COLUMN IF NOT EXISTS policy_version TEXT NOT NULL DEFAULT '';
ALTER TABLE operations ADD COLUMN IF NOT EXISTS patch_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE operations ADD COLUMN IF NOT EXISTS effective_values_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE operations ADD COLUMN IF NOT EXISTS target_operation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE operations ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '';
