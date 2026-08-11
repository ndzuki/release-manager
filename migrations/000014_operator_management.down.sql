-- REQ-053: drop the operator enrollment lifecycle columns and indexes.

DROP INDEX IF EXISTS operators_cluster_active_uq;
DROP INDEX IF EXISTS operators_customer_active_name_uq;
DROP INDEX IF EXISTS enrollment_tokens_pending_cluster_uq;

ALTER TABLE sessions
    DROP COLUMN closed_at,
    DROP COLUMN status_reason,
    DROP COLUMN cluster_id,
    DROP COLUMN customer_id;

ALTER TABLE operators
    DROP COLUMN revoke_reason,
    DROP COLUMN superseded_at;

ALTER TABLE enrollment_tokens
    DROP COLUMN replaced_by_id,
    DROP COLUMN revoked_at,
    DROP COLUMN created_by_display_name,
    DROP COLUMN state,
    DROP COLUMN operator_name;
