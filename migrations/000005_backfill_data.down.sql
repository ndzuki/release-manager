DELETE FROM bundle_candidate_artifacts bca
USING candidate_artifacts ca
WHERE bca.candidate_artifact_id = ca.id
  AND ca.bundle_id IS NOT NULL
  AND ca.bundle_id <> '';
UPDATE operations SET terminal_at = NULL;
