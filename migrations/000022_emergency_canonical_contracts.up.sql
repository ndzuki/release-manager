-- REQ-079 canonical emergency contracts: convergence bindings on
-- values_revisions (Postgres array columns, D15) and the application
-- settings key/value store (kill switch + timeout, D6/D16).

ALTER TABLE values_revisions
    ADD COLUMN IF NOT EXISTS convergence_task_ids uuid[] NOT NULL DEFAULT '{}';

ALTER TABLE values_revisions
    ADD COLUMN IF NOT EXISTS locked_paths text[] NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
