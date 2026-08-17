ALTER TABLE release_bundles
    DROP CONSTRAINT IF EXISTS release_bundles_status_check;

ALTER TABLE release_bundles
    ADD CONSTRAINT release_bundles_status_check
    CHECK (status IN ('received', 'validated', 'rejected', 'archived'));
