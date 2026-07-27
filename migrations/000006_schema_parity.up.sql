-- Bring the PostgreSQL schema to parity with the complete Store contract.

-- Release definitions and values approval workflow.
ALTER TABLE release_definitions
    ALTER COLUMN current_bundle_id DROP NOT NULL,
    ALTER COLUMN current_bundle_id DROP DEFAULT,
    ADD COLUMN owner_organization_id TEXT,
    ADD COLUMN approved_revision_id TEXT;

ALTER TABLE values_revisions
    ADD COLUMN version BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN created_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN approved_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN approved_at TIMESTAMPTZ,
    ADD COLUMN rejected_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN rejection_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN state_version BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN created_by_user_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN submitted_at TIMESTAMPTZ,
    ADD COLUMN decided_at TIMESTAMPTZ;

UPDATE values_revisions
SET state_version = CASE WHEN version > 0 THEN version ELSE 1 END
WHERE state_version = 0;

UPDATE values_revisions
SET created_by_user_id = created_by
WHERE created_by_user_id = '';

CREATE TABLE values_revision_decisions (
    id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES values_revisions(id) ON DELETE RESTRICT,
    release_definition_id TEXT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('submitted', 'approved', 'rejected')),
    from_state TEXT NOT NULL,
    to_state TEXT NOT NULL,
    actor_user_id TEXT NOT NULL,
    actor_org_id TEXT NOT NULL,
    actor_role TEXT NOT NULL DEFAULT '',
    comment TEXT,
    reason TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT '',
    idempotency_key_hash TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_values_revision_decisions_revision
    ON values_revision_decisions(revision_id, created_at);

CREATE TABLE idempotency_records (
    scope TEXT NOT NULL,
    text_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_ref BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY(scope, text_key)
);
CREATE INDEX idx_idempotency_records_expires_at
    ON idempotency_records(expires_at);

CREATE TABLE audit_outbox (
    id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    payload_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    delivered BOOLEAN NOT NULL DEFAULT FALSE,
    delivered_at TIMESTAMPTZ
);

CREATE TABLE notification_outbox (
    id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    payload_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    delivered BOOLEAN NOT NULL DEFAULT FALSE,
    delivered_at TIMESTAMPTZ
);

UPDATE values_revisions AS current
SET status = 'superseded',
    state_version = state_version + 1,
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE status = 'approved'
  AND EXISTS (
      SELECT 1
      FROM values_revisions AS newer
      WHERE newer.release_definition_id = current.release_definition_id
        AND newer.status = 'approved'
        AND (
            newer.revision > current.revision
            OR (newer.revision = current.revision AND newer.id > current.id)
        )
  );

CREATE UNIQUE INDEX ux_vr_one_approved_per_def
    ON values_revisions(release_definition_id)
    WHERE status = 'approved';
CREATE UNIQUE INDEX ux_vr_one_pending_per_def
    ON values_revisions(release_definition_id)
    WHERE status = 'pending_approval';

-- Cluster optimistic locking and verification provenance.
ALTER TABLE clusters
    ADD COLUMN version BIGINT NOT NULL DEFAULT 1;

ALTER TABLE verification_records
    ADD COLUMN root_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN key_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN revocation_epoch BIGINT NOT NULL DEFAULT 0;

-- Audit export jobs.
CREATE TABLE audit_exports (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL DEFAULT '',
    since TIMESTAMPTZ NOT NULL,
    until TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_audit_exports_organization
    ON audit_exports(organization_id, created_at);

-- Trust roots and versioned trust policy metadata.
CREATE TABLE trust_roots (
    id TEXT PRIMARY KEY,
    environment TEXT NOT NULL,
    key_id TEXT NOT NULL DEFAULT '',
    public_key_pem TEXT NOT NULL DEFAULT '',
    issuer TEXT NOT NULL DEFAULT '',
    subject_pattern TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    valid_from TIMESTAMPTZ NOT NULL,
    grace_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);
CREATE INDEX idx_trust_roots_environment
    ON trust_roots(environment, created_at);
CREATE INDEX idx_trust_roots_active
    ON trust_roots(environment, valid_from)
    WHERE state IN ('active', 'grace');

CREATE TABLE trust_policies (
    environment TEXT PRIMARY KEY,
    version BIGINT NOT NULL DEFAULT 0,
    revocation_epoch BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL
);

-- Vulnerability scan results and time-bounded exceptions.
CREATE TABLE scan_results (
    id TEXT PRIMARY KEY,
    artifact_digest TEXT NOT NULL,
    sbom_ref TEXT NOT NULL DEFAULT '',
    scanner TEXT NOT NULL DEFAULT '',
    result_version TEXT NOT NULL DEFAULT '',
    severity_json JSONB NOT NULL,
    findings_json JSONB NOT NULL,
    scanned_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_scan_results_artifact_scanner
    ON scan_results(artifact_digest, scanner, created_at DESC);

CREATE TABLE vulnerability_exceptions (
    id TEXT PRIMARY KEY,
    finding_id TEXT NOT NULL DEFAULT '',
    artifact_digest TEXT NOT NULL DEFAULT '',
    actor TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_vulnerability_exceptions_artifact
    ON vulnerability_exceptions(artifact_digest, created_at DESC);

-- Complete artifact and preflight lifecycle metadata and query indexes.
ALTER TABLE preflight_lifecycles
    ALTER COLUMN stages DROP DEFAULT,
    ALTER COLUMN stages TYPE JSONB
        USING CASE WHEN stages = '' THEN '[]'::jsonb ELSE stages::jsonb END,
    ALTER COLUMN stages SET DEFAULT '[]'::jsonb,
    ADD COLUMN error_code TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_candidate_artifacts_orphaned
    ON candidate_artifacts(created_at)
    WHERE bundle_id IS NULL;
CREATE INDEX idx_preflight_lifecycles_terminal
    ON preflight_lifecycles(operation_terminal_at);
