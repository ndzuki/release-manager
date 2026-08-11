DROP INDEX IF EXISTS operators_cert_serial_uq;
CREATE INDEX idx_operators_cert ON operators(cert_serial);
