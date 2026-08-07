DROP INDEX IF EXISTS idx_preflight_lifecycles_terminal;
DROP INDEX IF EXISTS idx_candidate_artifacts_orphaned;
ALTER TABLE preflight_lifecycles
    DROP COLUMN error_code,
    ALTER COLUMN stages DROP DEFAULT,
    ALTER COLUMN stages TYPE TEXT USING stages::text,
    ALTER COLUMN stages SET DEFAULT '';

DROP INDEX IF EXISTS idx_vulnerability_exceptions_artifact;
DROP TABLE IF EXISTS vulnerability_exceptions;

DROP INDEX IF EXISTS idx_scan_results_artifact_scanner;
DROP TABLE IF EXISTS scan_results;

DROP TABLE IF EXISTS trust_policies;
DROP INDEX IF EXISTS idx_trust_roots_active;
DROP INDEX IF EXISTS idx_trust_roots_environment;
DROP TABLE IF EXISTS trust_roots;

DROP INDEX IF EXISTS idx_audit_exports_organization;
DROP TABLE IF EXISTS audit_exports;

ALTER TABLE verification_records
    DROP COLUMN revocation_epoch,
    DROP COLUMN key_id,
    DROP COLUMN root_id;

ALTER TABLE clusters DROP COLUMN version;

DROP INDEX IF EXISTS ux_vr_one_pending_per_def;
DROP INDEX IF EXISTS ux_vr_one_approved_per_def;
DROP TABLE IF EXISTS notification_outbox;
DROP TABLE IF EXISTS audit_outbox;
DROP INDEX IF EXISTS idx_idempotency_records_expires_at;
DROP TABLE IF EXISTS idempotency_records;
DROP INDEX IF EXISTS idx_values_revision_decisions_revision;
DROP TABLE IF EXISTS values_revision_decisions;

ALTER TABLE values_revisions
    DROP COLUMN decided_at,
    DROP COLUMN submitted_at,
    DROP COLUMN created_by_user_id,
    DROP COLUMN state_version,
    DROP COLUMN rejection_reason,
    DROP COLUMN rejected_by,
    DROP COLUMN approved_at,
    DROP COLUMN approved_by,
    DROP COLUMN created_by,
    DROP COLUMN version;

UPDATE release_definitions
SET current_bundle_id = ''
WHERE current_bundle_id IS NULL;

ALTER TABLE release_definitions
    DROP COLUMN approved_revision_id,
    DROP COLUMN owner_organization_id,
    ALTER COLUMN current_bundle_id SET DEFAULT '',
    ALTER COLUMN current_bundle_id SET NOT NULL;
