-- Restore the empty-string sentinel defined by baseline migration 000002.
UPDATE release_bundles
SET archived_from_status = ''
WHERE status = 'archived'
  AND archived_from_status = 'validated';
