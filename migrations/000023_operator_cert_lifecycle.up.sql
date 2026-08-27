-- TASK-015: operator enrollment identity lifecycle (REQ-015 v1.1).
-- 1. operators.certificate_expires_at is the renew-window authority
--    (REQ-015 事务边界 3: RenewCertificate updates serial + expiry together).
-- 2. Drop the legacy plaintext-capable enrollment_tokens.token column.
--    Writers already stored only the SHA-256 hash in it (TASK-053); the
--    authoritative lookup column is token_hash. REQ-015 安全边界 forbids
--    persisting the plaintext token in any column.

ALTER TABLE operators ADD COLUMN certificate_expires_at TIMESTAMPTZ;

ALTER TABLE enrollment_tokens DROP COLUMN token;
