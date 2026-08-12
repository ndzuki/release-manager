ALTER TABLE candidate_artifacts
    ADD COLUMN validated_at TIMESTAMPTZ,
    ADD COLUMN source_id TEXT NOT NULL DEFAULT '';

ALTER TABLE release_definitions
    ADD COLUMN hpa_managed BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN max_emergency_replicas INTEGER NOT NULL DEFAULT 100,
    ADD COLUMN approved_annotation_keys JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN promotion_mappings JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE emergency_intents (
    id TEXT NOT NULL PRIMARY KEY,
    release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE RESTRICT,
    command_id TEXT NOT NULL UNIQUE,
    action TEXT NOT NULL CHECK (action IN ('set_container_image','set_replicas','set_approved_annotation')),
    workload_kind TEXT NOT NULL,
    workload_name TEXT NOT NULL,
    workload_namespace TEXT NOT NULL,
    workload_uid TEXT NOT NULL,
    container TEXT,
    artifact_id TEXT,
    image_reference TEXT,
    target_replicas INTEGER,
    annotation_scope TEXT,
    annotation_entries JSONB,
    convergence TEXT NOT NULL DEFAULT 'require_promotion'
        CHECK (convergence IN ('require_promotion','revert_on_next_reconcile')),
    promotion_paths JSONB,
    before_snapshot JSONB,
    after_snapshot JSONB,
    delivery_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (delivery_status IN ('pending','queued','delivered','persisted')),
    last_delivery_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ei_operation ON emergency_intents(operation_id);
CREATE INDEX idx_ei_command ON emergency_intents(command_id);
CREATE INDEX idx_ei_definition ON emergency_intents(release_definition_id, created_at DESC);
CREATE INDEX idx_ei_active_locks ON emergency_intents(release_definition_id, workload_kind, workload_name)
    WHERE delivery_status != 'persisted';

CREATE TABLE convergence_tasks (
    id TEXT NOT NULL PRIMARY KEY,
    operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE RESTRICT,
    release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE RESTRICT,
    action TEXT NOT NULL,
    target_summary TEXT NOT NULL,
    reason TEXT NOT NULL,
    promotion_paths JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending_promotion'
        CHECK (status IN ('pending_promotion','converged')),
    active_revision_id TEXT,
    active_revision_status TEXT,
    last_rejection_reason TEXT,
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    converged_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ct_definition ON convergence_tasks(release_definition_id, status);
CREATE UNIQUE INDEX ux_ct_op ON convergence_tasks(operation_id);
