-- ADR-018: cert serial (sha256(certDER)[:10] hex) is the identity authority.
-- A unique index makes an 80-bit DER-hash collision fail enrollment instead of
-- binding two operators to one certificate (REQ-015 v1.1).
DROP INDEX IF EXISTS idx_operators_cert;
CREATE UNIQUE INDEX operators_cert_serial_uq ON operators(cert_serial);
