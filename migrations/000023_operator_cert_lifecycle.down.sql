-- TASK-015 rollback: restore the legacy enrollment_tokens.token column and
-- drop certificate_expires_at. The plaintext token cannot be recovered from
-- the hash — the restored column stays empty (structural rollback only).

ALTER TABLE enrollment_tokens ADD COLUMN token TEXT NOT NULL DEFAULT '';

ALTER TABLE operators DROP COLUMN certificate_expires_at;
