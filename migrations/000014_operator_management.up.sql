-- REQ-053: operator enrollment lifecycle state, supersession metadata, and
-- session closure tracking for the PostgreSQL schema (parity with SQLite).

-- Enrollment token lifecycle state replaces the legacy `used` boolean.
ALTER TABLE enrollment_tokens
    ADD COLUMN operator_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN state TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN created_by_display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN revoked_at TIMESTAMPTZ,
    ADD COLUMN replaced_by_id TEXT NOT NULL DEFAULT '';

-- Backfill the state column from the legacy boolean.
UPDATE enrollment_tokens SET state = 'used' WHERE used = TRUE;

-- At most one pending token per cluster.
CREATE UNIQUE INDEX enrollment_tokens_pending_cluster_uq
    ON enrollment_tokens(cluster_id)
    WHERE state = 'pending';

-- Operator supersession and revocation reason.
ALTER TABLE operators
    ADD COLUMN superseded_at TIMESTAMPTZ,
    ADD COLUMN revoke_reason TEXT NOT NULL DEFAULT '';

-- At most one active operator per (customer, name) and per cluster.
CREATE UNIQUE INDEX operators_customer_active_name_uq
    ON operators(customer_id, operator_name)
    WHERE status = 'active';
CREATE UNIQUE INDEX operators_cluster_active_uq
    ON operators(cluster_id)
    WHERE status = 'active';

-- Session customer linkage and lifecycle closure.
ALTER TABLE sessions
    ADD COLUMN customer_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN cluster_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN status_reason TEXT,
    ADD COLUMN closed_at TIMESTAMPTZ;
