DROP TABLE IF EXISTS rollout_trackings;
DROP TABLE IF EXISTS operation_execution_results;

ALTER TABLE release_inventory DROP COLUMN IF EXISTS last_operation_id;
ALTER TABLE release_inventory DROP COLUMN IF EXISTS observed_manifest_digest;
ALTER TABLE release_inventory DROP COLUMN IF EXISTS observed_effective_values_digest;
ALTER TABLE release_inventory DROP COLUMN IF EXISTS observed_chart_digest;
ALTER TABLE release_inventory DROP COLUMN IF EXISTS observed_bundle_digest;
