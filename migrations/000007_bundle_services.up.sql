ALTER TABLE release_bundles
    ADD COLUMN signature_digest TEXT NOT NULL DEFAULT '',
    ADD COLUMN sbom_digest TEXT NOT NULL DEFAULT '',
    ADD COLUMN provenance_digest TEXT NOT NULL DEFAULT '';

CREATE TABLE release_bundle_image_bindings (
    bundle_id TEXT NOT NULL REFERENCES release_bundles(id) ON DELETE CASCADE,
    ref TEXT NOT NULL,
    digest TEXT NOT NULL,
    values_path TEXT NOT NULL,
    value_kind TEXT NOT NULL CHECK (value_kind IN ('FULL_REFERENCE', 'REPOSITORY', 'TAG', 'DIGEST')),
    position INTEGER NOT NULL DEFAULT 0,
    UNIQUE(bundle_id, values_path)
);
CREATE INDEX idx_bundle_images_digest ON release_bundle_image_bindings(digest);

INSERT INTO release_bundle_image_bindings (bundle_id, ref, digest, values_path, value_kind, position)
SELECT b.id,
       image->>'Ref',
       image->>'Digest',
       image->>'ValuesPath',
       COALESCE(NULLIF(image->>'ValueKind', ''), 'FULL_REFERENCE'),
       ordinality - 1
FROM release_bundles AS b
CROSS JOIN LATERAL jsonb_array_elements(b.images) WITH ORDINALITY AS entries(image, ordinality)
WHERE COALESCE(image->>'ValuesPath', '') <> '';

CREATE TABLE candidate_artifacts_v2 (
    id TEXT PRIMARY KEY,
    artifact_type TEXT NOT NULL CHECK (artifact_type IN ('image', 'chart', 'sbom', 'provenance', 'signature')),
    digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    orphaned_at TIMESTAMPTZ,
    UNIQUE(digest, artifact_type)
);

INSERT INTO candidate_artifacts_v2 (id, artifact_type, digest, created_at, last_seen_at, orphaned_at)
SELECT id, lower(artifact_type), digest, created_at, last_seen_at, orphaned_at
FROM candidate_artifacts;

CREATE TABLE bundle_candidate_artifacts_v2 (
    bundle_id TEXT NOT NULL REFERENCES release_bundles(id) ON DELETE CASCADE,
    artifact_id TEXT NOT NULL REFERENCES candidate_artifacts_v2(id) ON DELETE CASCADE,
    linked_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY(bundle_id, artifact_id)
);

INSERT INTO bundle_candidate_artifacts_v2 (bundle_id, artifact_id, linked_at)
SELECT bundle_id, candidate_artifact_id, CURRENT_TIMESTAMP
FROM bundle_candidate_artifacts;

INSERT INTO bundle_candidate_artifacts_v2 (bundle_id, artifact_id, linked_at)
SELECT bundle_id, id, CURRENT_TIMESTAMP
FROM candidate_artifacts
WHERE bundle_id IS NOT NULL
ON CONFLICT DO NOTHING;

CREATE TABLE candidate_artifact_locations (
    artifact_id TEXT NOT NULL REFERENCES candidate_artifacts_v2(id) ON DELETE CASCADE,
    ref TEXT NOT NULL,
    source_id TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (artifact_id, ref)
);

INSERT INTO candidate_artifact_locations (artifact_id, ref, source_id, first_seen_at, last_seen_at)
SELECT id, ref, 'legacy', created_at, last_seen_at
FROM candidate_artifacts
WHERE ref <> '';

DROP TABLE bundle_candidate_artifacts;
DROP TABLE candidate_artifacts;
ALTER TABLE candidate_artifacts_v2 RENAME TO candidate_artifacts;
ALTER TABLE bundle_candidate_artifacts_v2 RENAME TO bundle_candidate_artifacts;

CREATE INDEX idx_ca_digest_type ON candidate_artifacts(digest, artifact_type);
CREATE INDEX idx_ca_orphaned ON candidate_artifacts(orphaned_at);
CREATE INDEX idx_cal_artifact ON candidate_artifact_locations(artifact_id);

CREATE TABLE artifact_events (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    raw_payload TEXT NOT NULL,
    payload_sha256 TEXT NOT NULL,
    artifact_type TEXT NOT NULL,
    repository TEXT NOT NULL,
    UNIQUE(source_id, event_id)
);
CREATE INDEX idx_ae_source_event ON artifact_events(source_id, event_id);

CREATE TABLE bundle_aliases (
    alias TEXT PRIMARY KEY,
    canonical_bundle_id TEXT NOT NULL REFERENCES release_bundles(id) ON DELETE CASCADE,
    alias_type TEXT NOT NULL CHECK (alias_type IN ('legacy_id', 'legacy_digest')),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE bundle_validation_outbox (
    id TEXT PRIMARY KEY,
    bundle_id TEXT NOT NULL REFERENCES release_bundles(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'failed', 'completed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error_code TEXT NOT NULL DEFAULT '',
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE(bundle_id)
);
CREATE INDEX idx_validation_next ON bundle_validation_outbox(status, next_attempt_at);
CREATE INDEX idx_bundles_status_created ON release_bundles(status, created_at);
