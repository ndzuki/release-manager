DROP INDEX IF EXISTS ux_ct_op;
DROP INDEX IF EXISTS idx_ct_definition;
DROP TABLE IF EXISTS convergence_tasks;

DROP INDEX IF EXISTS idx_ei_active_locks;
DROP INDEX IF EXISTS idx_ei_definition;
DROP INDEX IF EXISTS idx_ei_command;
DROP INDEX IF EXISTS idx_ei_operation;
DROP TABLE IF EXISTS emergency_intents;

ALTER TABLE release_definitions
    DROP COLUMN promotion_mappings,
    DROP COLUMN approved_annotation_keys,
    DROP COLUMN max_emergency_replicas,
    DROP COLUMN hpa_managed;

ALTER TABLE candidate_artifacts
    DROP COLUMN source_id,
    DROP COLUMN validated_at;
