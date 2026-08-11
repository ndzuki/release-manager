-- REQ-031 PostgreSQL contract: notification_jobs in the release_notifier
-- database (per-authority, REQ-070/REQ-065). Column set mirrors the SQLite
-- schema (internal/store/sqlite/db.go) with PostgreSQL types: TIMESTAMPTZ
-- time columns, JSONB metadata, table-level UNIQUE on the idempotency key.
CREATE TABLE notification_jobs (
    id             TEXT PRIMARY KEY,
    operation_id   TEXT NOT NULL DEFAULT '',
    channel        TEXT NOT NULL DEFAULT 'webhook',
    recipient      TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'pending',
    attempts       INTEGER NOT NULL DEFAULT 0,
    retry_count    INTEGER NOT NULL DEFAULT 0,
    max_retries    INTEGER NOT NULL DEFAULT 3,
    error_code     TEXT NOT NULL DEFAULT '',
    next_retry_at  TIMESTAMPTZ,
    last_error     TEXT NOT NULL DEFAULT '',
    sent_at        TIMESTAMPTZ,
    dead_letter_at TIMESTAMPTZ,
    metadata       JSONB NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL,
    UNIQUE (operation_id, channel, recipient)
);

CREATE INDEX idx_notification_jobs_status ON notification_jobs(status);
CREATE INDEX idx_notification_jobs_created_at ON notification_jobs(created_at);
