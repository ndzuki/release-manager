UPDATE operations
SET terminal_at = updated_at
WHERE status IN ('succeeded', 'failed', 'cancelled', 'timeout') AND terminal_at IS NULL;

INSERT INTO bundle_candidate_artifacts(bundle_id, candidate_artifact_id)
SELECT bundle_id, id
FROM candidate_artifacts
WHERE bundle_id IS NOT NULL AND bundle_id <> ''
ON CONFLICT DO NOTHING;
