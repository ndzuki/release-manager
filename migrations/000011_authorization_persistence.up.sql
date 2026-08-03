CREATE TABLE authorization_source_version (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id = TRUE),
    version BIGINT NOT NULL DEFAULT 0
);
INSERT INTO authorization_source_version (id, version) VALUES (TRUE, 0)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE capability_grants (
    organization_id TEXT NOT NULL,
    subject TEXT NOT NULL,
    action TEXT NOT NULL,
    granted_by TEXT NOT NULL,
    revoked BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (organization_id, subject, action)
);
CREATE INDEX idx_capability_grants_active
    ON capability_grants(organization_id, subject, action)
    WHERE revoked = FALSE;

CREATE TABLE casbin_rule (
    id BIGSERIAL PRIMARY KEY,
    ptype VARCHAR(12) NOT NULL,
    v0 VARCHAR(255) NOT NULL DEFAULT '',
    v1 VARCHAR(255) NOT NULL DEFAULT '',
    v2 VARCHAR(255) NOT NULL DEFAULT '',
    v3 VARCHAR(255) NOT NULL DEFAULT '',
    v4 VARCHAR(255) NOT NULL DEFAULT '',
    v5 VARCHAR(255) NOT NULL DEFAULT ''
);
CREATE INDEX idx_casbin_rule_lookup ON casbin_rule(ptype, v0, v1, v2, v3);

CREATE TABLE policy_version (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id = TRUE),
    version BIGINT NOT NULL DEFAULT 0
);
INSERT INTO policy_version (id, version) VALUES (TRUE, 0)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE authorization_checkpoints (
    organization_id TEXT NOT NULL,
    customer_id TEXT NOT NULL,
    source_version BIGINT NOT NULL DEFAULT 0,
    policy_version BIGINT NOT NULL DEFAULT 0,
    fresh BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (organization_id, customer_id)
);
