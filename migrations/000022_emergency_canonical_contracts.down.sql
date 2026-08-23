ALTER TABLE values_revisions DROP COLUMN IF EXISTS convergence_task_ids;
ALTER TABLE values_revisions DROP COLUMN IF EXISTS locked_paths;
DROP TABLE IF EXISTS app_settings;
