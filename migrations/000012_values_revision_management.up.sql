-- Align ValuesRevision content versioning and add convergence prepare sessions.

UPDATE values_revisions
SET version = revision;

UPDATE values_revisions
SET parent_revision_id = NULL
WHERE parent_revision_id = '';

ALTER TABLE values_revisions
    DROP COLUMN revision,
    ALTER COLUMN parent_revision_id DROP NOT NULL,
    ALTER COLUMN parent_revision_id DROP DEFAULT;

ALTER TABLE values_revisions
    DROP CONSTRAINT IF EXISTS values_revisions_release_definition_id_fkey,
    DROP CONSTRAINT IF EXISTS values_revisions_parent_revision_id_fkey;
ALTER TABLE values_revisions
    ADD CONSTRAINT values_revisions_release_definition_id_fkey
        FOREIGN KEY (release_definition_id) REFERENCES release_definitions(id) ON DELETE RESTRICT,
    ADD CONSTRAINT values_revisions_parent_revision_id_fkey
        FOREIGN KEY (parent_revision_id) REFERENCES values_revisions(id) ON DELETE RESTRICT;

DROP INDEX IF EXISTS ux_vr_def_version;
CREATE UNIQUE INDEX ux_vr_def_version
    ON values_revisions(release_definition_id, version);

ALTER TABLE values_revisions
    DROP CONSTRAINT IF EXISTS values_revisions_status_check;
ALTER TABLE values_revisions
    ADD CONSTRAINT values_revisions_status_check
    CHECK (status IN ('draft', 'pending_approval', 'approved', 'rejected', 'superseded', 'discarded'));

ALTER TABLE values_revision_decisions
    DROP CONSTRAINT IF EXISTS values_revision_decisions_action_check;
ALTER TABLE values_revision_decisions
    ADD CONSTRAINT values_revision_decisions_action_check
    CHECK (action IN ('submitted', 'approved', 'rejected', 'discarded'));

CREATE TABLE convergence_prepare_sessions (
    token_hash            TEXT PRIMARY KEY,
    actor_user_id         TEXT NOT NULL,
    organization_id       TEXT NOT NULL,
    release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE RESTRICT,
    parent_revision_id    TEXT REFERENCES values_revisions(id) ON DELETE RESTRICT,
    parent_version        BIGINT NOT NULL,
    task_ids              JSONB NOT NULL,
    locked_paths          JSONB NOT NULL,
    locked_path_hash      TEXT NOT NULL,
    expires_at            TIMESTAMPTZ NOT NULL,
    consumed_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((parent_revision_id IS NULL AND parent_version = 0)
        OR (parent_revision_id IS NOT NULL AND parent_version > 0))
);
CREATE INDEX ix_cps_expiry ON convergence_prepare_sessions(expires_at);
