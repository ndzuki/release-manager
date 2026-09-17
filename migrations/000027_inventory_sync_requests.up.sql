-- REQ-088/REQ-089 inventory sync request tracking (schema parity fix).
--
-- internal/store/postgres/inventory_sync_requests.go queries this table, and the
-- SQLite engine creates it in internal/store/sqlite/db.go, but no PostgreSQL
-- migration ever created it. On PostgreSQL the production path therefore failed
-- with "relation inventory_sync_requests does not exist" the first time an
-- inventory sync request was recorded, while SQLite (dev/test) worked — the
-- exact dual-engine divergence the schema-parity rule exists to prevent. The
-- gap surfaced by running the migration integration test against a real
-- PostgreSQL (TestRunMigratesCurrentSQLiteSchemaEndToEnd), which compares the
-- SQLite source schema with the migrated PostgreSQL schema.

CREATE TABLE IF NOT EXISTS inventory_sync_requests (
    id          TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL,
    cluster_id  TEXT NOT NULL,
    operator_id TEXT NOT NULL,
    command_id  TEXT NOT NULL UNIQUE,
    status      TEXT NOT NULL DEFAULT 'pending',
    last_error  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_inventory_sync_requests_cluster
    ON inventory_sync_requests(customer_id, cluster_id, status);

-- At most one in-flight request per cluster: the partial index mirrors the
-- SQLite DDL exactly, including the status set it covers.
CREATE UNIQUE INDEX IF NOT EXISTS idx_inventory_sync_requests_active_cluster
    ON inventory_sync_requests(customer_id, cluster_id)
    WHERE status IN ('pending', 'running');
