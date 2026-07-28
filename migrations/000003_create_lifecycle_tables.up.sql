CREATE TABLE candidate_artifacts (
    id TEXT PRIMARY KEY,
    artifact_type TEXT NOT NULL,
    ref TEXT NOT NULL DEFAULT '',
    digest TEXT NOT NULL,
    bundle_id TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    orphaned_at TIMESTAMPTZ,
    UNIQUE(digest, artifact_type)
);
CREATE TABLE bundle_candidate_artifacts (
    bundle_id TEXT NOT NULL REFERENCES release_bundles(id) ON DELETE CASCADE,
    candidate_artifact_id TEXT NOT NULL REFERENCES candidate_artifacts(id) ON DELETE CASCADE,
    PRIMARY KEY(bundle_id, candidate_artifact_id)
);
CREATE TABLE preflight_lifecycles (
    id TEXT PRIMARY KEY,
    operation_id TEXT UNIQUE,
    operation_terminal_at TIMESTAMPTZ,
    stages TEXT NOT NULL DEFAULT '',
    overall TEXT NOT NULL DEFAULT 'running',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE cleanup_idempotency (
    idempotency_key TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL
);
