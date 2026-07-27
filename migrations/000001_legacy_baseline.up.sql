CREATE TABLE release_definitions (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    customer_id TEXT NOT NULL,
    cluster_id TEXT NOT NULL,
    namespace TEXT NOT NULL DEFAULT '',
    release_name TEXT NOT NULL,
    chart_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'draft',
    optimistic_version BIGINT NOT NULL DEFAULT 0,
    created_by TEXT NOT NULL DEFAULT '',
    current_bundle_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE(customer_id, cluster_id, namespace, release_name)
);

CREATE TABLE release_definition_events (
    id TEXT PRIMARY KEY,
    definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_release_definition_events_definition ON release_definition_events(definition_id, created_at);

CREATE TABLE values_revisions (
    id TEXT PRIMARY KEY,
    release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'draft',
    "values" BYTEA NOT NULL,
    digest TEXT NOT NULL DEFAULT '',
    parent_revision_id TEXT NOT NULL DEFAULT '',
    secret_refs BYTEA,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_values_def ON values_revisions(release_definition_id);
CREATE INDEX idx_values_digest ON values_revisions(release_definition_id, digest);

CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    operation_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL UNIQUE,
    request_hash TEXT NOT NULL,
    state_version BIGINT NOT NULL DEFAULT 0,
    bundle_id TEXT NOT NULL DEFAULT '',
    values_revision_id TEXT NOT NULL DEFAULT '',
    expected_revision BIGINT NOT NULL DEFAULT 0,
    target_revision BIGINT NOT NULL DEFAULT 0,
    values_patch BYTEA,
    actor JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deadline TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_operations_definition ON operations(release_definition_id, status);
CREATE INDEX idx_operations_idempotency ON operations(idempotency_key);
CREATE UNIQUE INDEX idx_operations_one_active_standard ON operations(release_definition_id)
WHERE operation_type <> 'EMERGENCY' AND status NOT IN ('succeeded', 'failed', 'cancelled', 'timeout');

CREATE TABLE customers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE clusters (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    customer_id TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    kubeconfig_ref TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_clusters_customer ON clusters(customer_id);

CREATE TABLE enrollment_tokens (
    id TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL,
    cluster_id TEXT NOT NULL,
    token TEXT NOT NULL UNIQUE,
    token_hash TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used BOOLEAN NOT NULL DEFAULT FALSE,
    used_at TIMESTAMPTZ,
    operator_id TEXT NOT NULL DEFAULT ''
);

CREATE TABLE operators (
    id TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL,
    cluster_id TEXT NOT NULL,
    operator_name TEXT NOT NULL DEFAULT '',
    cert_serial TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    superseded_by TEXT NOT NULL DEFAULT '',
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_operators_cert ON operators(cert_serial);
CREATE INDEX idx_operators_name ON operators(operator_name);
CREATE INDEX idx_operators_cluster ON operators(cluster_id, status);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    operator_id TEXT NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
    instance_id TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    capabilities JSONB NOT NULL DEFAULT '{}'::jsonb,
    active_config_version TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'online',
    started_at TIMESTAMPTZ NOT NULL,
    last_heartbeat TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_sessions_instance ON sessions(operator_id, instance_id);
CREATE UNIQUE INDEX idx_sessions_one_active_operator ON sessions(operator_id) WHERE status IN ('online', 'suspect');
CREATE INDEX idx_sessions_operator ON sessions(operator_id, status);

CREATE TABLE outbox (
    id TEXT PRIMARY KEY,
    command_id TEXT NOT NULL DEFAULT '',
    operation_id TEXT NOT NULL DEFAULT '',
    operation_type TEXT NOT NULL DEFAULT '',
    operator_id TEXT NOT NULL,
    payload BYTEA NOT NULL DEFAULT ''::bytea,
    status TEXT NOT NULL DEFAULT 'pending',
    max_inflight BIGINT NOT NULL DEFAULT 1,
    sequence BIGINT NOT NULL DEFAULT 0,
    result_json TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    delivered_at TIMESTAMPTZ,
    acked_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_operator_status ON outbox(operator_id, status);
CREATE INDEX idx_outbox_sequence ON outbox(sequence);
CREATE INDEX idx_outbox_command_id ON outbox(command_id);

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_users_provider_subject ON users(provider, subject) WHERE provider <> '' AND subject <> '';

CREATE TABLE auth_sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_family TEXT NOT NULL,
    refresh_token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    revoked BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX idx_auth_sessions_family ON auth_sessions(token_family);
CREATE INDEX idx_auth_sessions_user ON auth_sessions(user_id);

CREATE TABLE organizations (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    optimistic_version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE organization_members (
    org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'viewer',
    optimistic_version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, user_id)
);

CREATE TABLE org_customer_bindings (
    id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    customer_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    optimistic_version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE(org_id, customer_id)
);
CREATE INDEX idx_bindings_org ON org_customer_bindings(org_id);

CREATE TABLE organization_customer_binding_events (
    id TEXT PRIMARY KEY,
    binding_id TEXT NOT NULL REFERENCES org_customer_bindings(id) ON DELETE CASCADE,
    org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    customer_id TEXT NOT NULL,
    status TEXT NOT NULL,
    optimistic_version BIGINT NOT NULL,
    changed_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_binding_events_binding ON organization_customer_binding_events(binding_id, changed_at);
CREATE UNIQUE INDEX idx_binding_events_version ON organization_customer_binding_events(binding_id, optimistic_version);

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    actor_kind TEXT NOT NULL DEFAULT 'system',
    actor_id TEXT NOT NULL DEFAULT '',
    organization_id TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT '',
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    change_summary TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_audit_events_actor ON audit_events(actor_kind, actor_id);
CREATE INDEX idx_audit_events_resource ON audit_events(resource_type, resource_id);
CREATE INDEX idx_audit_events_created ON audit_events(created_at);

CREATE TABLE notification_jobs (
    id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL DEFAULT '',
    channel TEXT NOT NULL DEFAULT 'webhook',
    recipient TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    attempts BIGINT NOT NULL DEFAULT 0,
    retry_count BIGINT NOT NULL DEFAULT 0,
    max_retries BIGINT NOT NULL DEFAULT 3,
    error_code TEXT NOT NULL DEFAULT '',
    next_retry_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    sent_at TIMESTAMPTZ,
    dead_letter_at TIMESTAMPTZ,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_notification_jobs_status ON notification_jobs(status);
CREATE UNIQUE INDEX idx_notification_jobs_dedup ON notification_jobs(operation_id, channel, recipient);

CREATE TABLE verification_records (
    id TEXT PRIMARY KEY,
    artifact_digest TEXT NOT NULL,
    policy_version TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    issuer TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_verification_records_digest_policy ON verification_records(artifact_digest, policy_version, created_at);

CREATE TABLE customer_events (
    id TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_customer_events_customer ON customer_events(customer_id, event_type);

CREATE TABLE operation_events (
    id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    operation_type TEXT NOT NULL,
    release_definition_id TEXT NOT NULL,
    old_status TEXT NOT NULL,
    new_status TEXT NOT NULL,
    state_version BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_operation_events_operation ON operation_events(operation_id);

CREATE TABLE cluster_routes (
    id TEXT PRIMARY KEY,
    cluster_id TEXT NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    artifact_type TEXT NOT NULL,
    mode TEXT NOT NULL,
    source_prefix TEXT NOT NULL DEFAULT '',
    target_prefix TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_cluster_routes_cluster ON cluster_routes(cluster_id, artifact_type);
CREATE UNIQUE INDEX idx_cluster_routes_unique ON cluster_routes(cluster_id, artifact_type, source_prefix);

CREATE TABLE release_inventory (
    customer_id TEXT NOT NULL,
    cluster_id TEXT NOT NULL,
    release_definition_id TEXT NOT NULL DEFAULT '',
    namespace TEXT NOT NULL DEFAULT '',
    release_name TEXT NOT NULL,
    chart TEXT NOT NULL DEFAULT '',
    chart_version TEXT NOT NULL DEFAULT '',
    revision BIGINT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT '',
    values_digest TEXT NOT NULL DEFAULT '',
    inventory_status TEXT NOT NULL DEFAULT 'active',
    last_sync_id TEXT NOT NULL DEFAULT '',
    snapshot_version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE(customer_id, cluster_id, namespace, release_name)
);
CREATE INDEX idx_inventory_cluster ON release_inventory(customer_id, cluster_id);
CREATE INDEX idx_inventory_status ON release_inventory(inventory_status);

CREATE TABLE inventory_sync_log (
    sync_id TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL,
    cluster_id TEXT NOT NULL,
    is_full_snapshot BOOLEAN NOT NULL DEFAULT FALSE,
    accepted_count BIGINT NOT NULL DEFAULT 0,
    missing_count BIGINT NOT NULL DEFAULT 0,
    snapshot_version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE release_bundles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    digest_alg TEXT NOT NULL DEFAULT 'sha256',
    digest_value TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'received',
    chart_ref TEXT NOT NULL DEFAULT '',
    chart_version TEXT NOT NULL DEFAULT '',
    chart_digest TEXT NOT NULL DEFAULT '',
    images JSONB NOT NULL DEFAULT '[]'::jsonb,
    git_commit TEXT NOT NULL DEFAULT '',
    pipeline_id TEXT NOT NULL DEFAULT '',
    signature_ref TEXT NOT NULL DEFAULT '',
    sbom_ref TEXT NOT NULL DEFAULT '',
    provenance_ref TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_release_bundles_digest ON release_bundles(digest_alg, digest_value);

CREATE TABLE preflight_results (
    id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL,
    routing_version TEXT NOT NULL DEFAULT '',
    bundle_digest TEXT NOT NULL,
    trust_policy_version TEXT NOT NULL DEFAULT '',
    sbom_policy_version TEXT NOT NULL DEFAULT '',
    result_json BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_preflight_results_key ON preflight_results(operation_id, routing_version, bundle_digest, trust_policy_version, sbom_policy_version);
