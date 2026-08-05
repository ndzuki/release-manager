DROP INDEX IF EXISTS ix_cps_expiry;
DROP TABLE IF EXISTS convergence_prepare_sessions;

ALTER TABLE values_revision_decisions
    DROP CONSTRAINT IF EXISTS values_revision_decisions_action_check;
ALTER TABLE values_revision_decisions
    ADD CONSTRAINT values_revision_decisions_action_check
    CHECK (action IN ('submitted', 'approved', 'rejected'));

ALTER TABLE values_revisions
    DROP CONSTRAINT IF EXISTS values_revisions_status_check;
ALTER TABLE values_revisions
    ADD CONSTRAINT values_revisions_status_check
    CHECK (status IN ('draft', 'pending_approval', 'approved', 'rejected', 'superseded'));

DROP INDEX IF EXISTS ux_vr_def_version;

ALTER TABLE values_revisions
    DROP CONSTRAINT IF EXISTS values_revisions_parent_revision_id_fkey,
    DROP CONSTRAINT IF EXISTS values_revisions_release_definition_id_fkey;
UPDATE values_revisions
SET parent_revision_id = ''
WHERE parent_revision_id IS NULL;
ALTER TABLE values_revisions
    ALTER COLUMN parent_revision_id SET DEFAULT '',
    ALTER COLUMN parent_revision_id SET NOT NULL,
    ADD COLUMN revision BIGINT;
UPDATE values_revisions
SET revision = version;
ALTER TABLE values_revisions
    ALTER COLUMN revision SET DEFAULT 1,
    ALTER COLUMN revision SET NOT NULL;
ALTER TABLE values_revisions
    ADD CONSTRAINT values_revisions_release_definition_id_fkey
        FOREIGN KEY (release_definition_id) REFERENCES release_definitions(id) ON DELETE CASCADE;
CREATE UNIQUE INDEX ux_vr_def_revision
    ON values_revisions(release_definition_id, revision);
