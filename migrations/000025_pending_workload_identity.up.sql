-- REQ-088 / TASK-088: orchestrator-side persistence for identity reports that
-- arrive before their release_inventory row exists (D2=A, D3=A, D6=A). The
-- operator reports authoritative kind/name/namespace/uid over the control
-- stream (REQ-085 D-110); when the inventory row is not yet known the report
-- is buffered here and replayed the moment SyncInventory's Upsert creates the
-- row (D5=A). The unique key (customer_id, cluster_id, namespace,
-- release_name) is the natural idempotency key (D6=A): re-reporting the same
-- release replaces the buffered four-tuple and refreshes created_at (TTL
-- start). A row is deleted as soon as it is bound; created_at older than the
-- TTL marks an orphan that the periodic sweep purges (D3=A).

CREATE TABLE IF NOT EXISTS pending_workload_identity (
    id                 TEXT PRIMARY KEY,
    customer_id        TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    cluster_id         TEXT NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    namespace          TEXT NOT NULL DEFAULT '',
    release_name       TEXT NOT NULL,
    workload_kind      TEXT NOT NULL DEFAULT '',
    workload_name      TEXT NOT NULL DEFAULT '',
    workload_namespace TEXT NOT NULL DEFAULT '',
    workload_uid       TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL,
    UNIQUE(customer_id, cluster_id, namespace, release_name)
);

CREATE INDEX IF NOT EXISTS idx_pending_workload_identity_cluster
    ON pending_workload_identity(customer_id, cluster_id);
