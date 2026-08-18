-- Baseline 000002 stores missing origins as an empty string; accept NULL for
-- databases created with a nullable archived_from_status column as well.
UPDATE release_bundles
SET archived_from_status = 'validated'
WHERE status = 'archived'
  AND (archived_from_status IS NULL OR archived_from_status = '');
