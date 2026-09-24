-- D1 rollback: recreate the cache-based preflight_results table exactly as
-- migrations/000001_legacy_baseline.up.sql:353-363 declared it — 8 columns plus
-- the unique idempotency index.
--
-- Structural rollback only: the dropped rows are not restored (there is no
-- production data, which is why the DROP was allowed). Recreating the table
-- without the Go accessors is deliberately still a valid rollback — it restores
-- the schema the forward migration removed.
CREATE TABLE preflight_results (
    id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL,
    routing_version TEXT NOT NULL DEFAULT '',
    bundle_digest TEXT NOT NULL,
    trust_policy_version TEXT NOT NULL DEFAULT '',
    sbom_policy_version TEXT NOT NULL DEFAULT '',
    result_json BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_preflight_results_key ON preflight_results(operation_id, routing_version, bundle_digest, trust_policy_version, sbom_policy_version);
