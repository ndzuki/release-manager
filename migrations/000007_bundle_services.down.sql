DROP INDEX IF EXISTS idx_bundles_status_created;
DROP INDEX IF EXISTS idx_validation_next;
DROP TABLE IF EXISTS bundle_validation_outbox;
DROP TABLE IF EXISTS bundle_aliases;
DROP INDEX IF EXISTS idx_ae_source_event;
DROP TABLE IF EXISTS artifact_events;

CREATE TABLE candidate_artifacts_v1 (
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

INSERT INTO candidate_artifacts_v1 (id, artifact_type, ref, digest, bundle_id, created_at, last_seen_at, orphaned_at)
SELECT ca.id,
       ca.artifact_type,
       COALESCE((
           SELECT location.ref
           FROM candidate_artifact_locations AS location
           WHERE location.artifact_id = ca.id
           ORDER BY location.last_seen_at DESC, location.ref
           LIMIT 1
       ), ''),
       ca.digest,
       (
           SELECT link.bundle_id
           FROM bundle_candidate_artifacts AS link
           WHERE link.artifact_id = ca.id
           ORDER BY link.linked_at DESC, link.bundle_id
           LIMIT 1
       ),
       ca.created_at,
       ca.last_seen_at,
       ca.orphaned_at
FROM candidate_artifacts AS ca;

CREATE TABLE bundle_candidate_artifacts_v1 (
    bundle_id TEXT NOT NULL REFERENCES release_bundles(id) ON DELETE CASCADE,
    candidate_artifact_id TEXT NOT NULL REFERENCES candidate_artifacts_v1(id) ON DELETE CASCADE,
    PRIMARY KEY(bundle_id, candidate_artifact_id)
);

INSERT INTO bundle_candidate_artifacts_v1 (bundle_id, candidate_artifact_id)
SELECT bundle_id, artifact_id
FROM bundle_candidate_artifacts;

DROP INDEX IF EXISTS idx_cal_artifact;
DROP TABLE candidate_artifact_locations;
DROP TABLE bundle_candidate_artifacts;
DROP TABLE candidate_artifacts;
ALTER TABLE candidate_artifacts_v1 RENAME TO candidate_artifacts;
ALTER TABLE bundle_candidate_artifacts_v1 RENAME TO bundle_candidate_artifacts;
CREATE INDEX idx_candidate_artifacts_orphaned
    ON candidate_artifacts(created_at)
    WHERE bundle_id IS NULL;

DROP INDEX IF EXISTS idx_bundle_images_digest;
DROP TABLE release_bundle_image_bindings;
ALTER TABLE release_bundles
    DROP COLUMN provenance_digest,
    DROP COLUMN sbom_digest,
    DROP COLUMN signature_digest;
