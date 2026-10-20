import { t, type MessageKey } from './messages';

/*
 * Display names for values the SERVER owns.
 *
 * The console kept re-implementing this: `OperationListPage` held a local STATE_LABELS
 * map, `OperationDetailPage` printed `operation.state` raw, the audit table printed
 * `event.status` raw, the emergency panel printed `opType`/`convergencePolicy` raw, and
 * the definitions table printed the raw definition status. So the same server value was
 * Chinese on one screen and English on another.
 *
 * One map per domain, one fallback rule (see statusLabel), and a test that pins both.
 */
export type StatusDomain =
  | 'operation'
  | 'operationType'
  | 'effect'
  | 'definition'
  | 'valuesRevision'
  | 'convergence'
  | 'convergenceStrategy'
  | 'audit'
  | 'export'
  | 'trustRoot'
  | 'localUser'
  | 'customer'
  | 'stage'
  | 'emergencyOperation'
  | 'ackStage'
  | 'bundle'
  | 'preflightStage'
  | 'auditAction'
  | 'auditResource';

const LABELS: Record<StatusDomain, Record<string, MessageKey>> = {
  // internal/store/store.go: OperationStatus
  operation: {
    pending: 'status.operation.pending',
    preflight: 'status.operation.preflight',
    queued: 'status.operation.queued',
    running: 'status.operation.running',
    cancelling: 'status.operation.cancelling',
    succeeded: 'status.operation.succeeded',
    failed: 'status.operation.failed',
    cancelled: 'status.operation.cancelled',
    timeout: 'status.operation.timeout',
  },
  // Operation kinds the console offers
  operationType: {
    INSTALL: 'status.operationType.install',
    UPGRADE: 'status.operationType.upgrade',
    ROLLBACK: 'status.operationType.rollback',
    EMERGENCY: 'status.operationType.emergency',
  },
  // internal/store/store.go: EmergencyEffectStatus
  effect: {
    UNKNOWN: 'status.effect.unknown',
    APPLIED: 'status.effect.applied',
    NOT_APPLIED: 'status.effect.notApplied',
    NOT_STARTED: 'status.effect.notStarted',
  },
  // internal/store/store.go: DefinitionStatus
  definition: {
    draft: 'status.definition.draft',
    active: 'status.definition.active',
    disabled: 'status.definition.disabled',
  },
  // Values revision status as returned by the orchestrator. `unknown` covers a value
  // the mapping could not read (open enum) so it is labelled, not left raw.
  valuesRevision: {
    draft: 'values.revision.status.draft',
    pending_approval: 'values.revision.status.pendingApproval',
    approved: 'values.revision.status.approved',
    rejected: 'values.revision.status.rejected',
    superseded: 'values.revision.status.superseded',
    discarded: 'values.revision.status.discarded',
    unknown: 'values.revision.status.unknown',
  },
  // Convergence task status. The server CHECK constraint allows only these two
  // (migrations/000008_emergency_change.up.sql), so `rejected` is NOT a value here.
  convergence: {
    pending_promotion: 'status.convergence.pendingPromotion',
    converged: 'status.convergence.converged',
  },
  // api/proto/orchestrator/v1/orchestrator.proto: ConvergenceStrategy
  convergenceStrategy: {
    REQUIRE_PROMOTION: 'status.convergenceStrategy.requirePromotion',
    REVERT_ON_NEXT_RECONCILE: 'status.convergenceStrategy.revertOnNextReconcile',
    CONVERGENCE_STRATEGY_UNSPECIFIED: 'status.convergenceStrategy.unspecified',
  },
  // Audit event outcome. The words come from the services that write them:
  // internal/orchestrator/audit.go ("succeeded"/"failed"),
  // internal/audit/audit_service_handler.go ("success"), and the filter list in
  // web/src/stores/audit.ts ("accepted").
  audit: {
    success: 'status.audit.success',
    succeeded: 'status.audit.succeeded',
    failed: 'status.audit.failed',
    accepted: 'status.audit.accepted',
  },
  // Audit event action and resource type. The console's filter offers these wire values
  // (web/src/stores/audit.ts), so the LABELLED form must not drift from them: a value the
  // map does not know falls through to the identifier, which is what the filter sent.
  auditAction: {
    create: 'status.auditAction.create',
    update: 'status.auditAction.update',
    delete: 'status.auditAction.delete',
    approve: 'status.auditAction.approve',
    reject: 'status.auditAction.reject',
    install: 'status.auditAction.install',
    upgrade: 'status.auditAction.upgrade',
    rollback: 'status.auditAction.rollback',
    emergency_change: 'status.auditAction.emergency_change',
    'export.created': 'status.auditAction.export.created',
    verify_trust: 'status.auditAction.verify_trust',
    login: 'status.auditAction.login',
    logout: 'status.auditAction.logout',
  },
  auditResource: {
    customer: 'status.auditResource.customer',
    cluster: 'status.auditResource.cluster',
    release: 'status.auditResource.release',
    operation: 'status.auditResource.operation',
    release_bundle: 'status.auditResource.release_bundle',
    trust_root: 'status.auditResource.trust_root',
    audit_export: 'status.auditResource.audit_export',
    organization: 'status.auditResource.organization',
  },
  // Audit export task status. Only "pending" is confirmed server-side (the export store
  // creates a receipt and never advances it); the others are accepted values with the
  // raw fallback covering anything else.
  export: {
    pending: 'status.export.pending',
    running: 'status.export.running',
    succeeded: 'status.export.succeeded',
    failed: 'status.export.failed',
  },
  // internal/store/store.go: TrustRootState
  trustRoot: {
    pending: 'status.trustRoot.pending',
    active: 'status.trustRoot.active',
    grace: 'status.trustRoot.grace',
    retired: 'status.trustRoot.retired',
    revoked: 'status.trustRoot.revoked',
  },
  // Emergency operation kinds (the set the console offers for an emergency change)
  emergencyOperation: {
    SET_CONTAINER_IMAGE: 'status.emergencyOperation.setContainerImage',
    SET_REPLICAS: 'status.emergencyOperation.setReplicas',
    SET_APPROVED_ANNOTATION: 'status.emergencyOperation.setAnnotation',
    REVERT_ON_NEXT_RECONCILE: 'status.emergencyOperation.revert',
  },
  // Local account status (api/proto/auth/v1/auth.proto: active | pending | disabled).
  // `unknown` is the console's own state for a value it could not read, not a server one.
  localUser: {
    active: 'localUser.status.active',
    pending: 'localUser.status.pending',
    disabled: 'localUser.status.disabled',
    unknown: 'localUser.status.unknown',
  },
  // Customer lifecycle status (api/proto/common/v1/domain.proto: Customer.status is a
  // free-form string; only `active` and `disabled` are states this console can read).
  customer: {
    active: 'status.active',
    disabled: 'status.disabled',
    unknown: 'status.unknown',
  },
  // internal/orchestrator/preflight/result.go: StageStatus
  stage: {
    passed: 'status.stage.passed',
    failed: 'status.stage.failed',
    skipped: 'status.stage.skipped',
    timeout: 'status.stage.timeout',
    cancelled: 'status.stage.cancelled',
  },
  // internal/orchestrator/preflight/stage.go: StageName. The stage id is a wire value,
  // so the panel labels it instead of printing `runtime_pull` to the operator.
  preflightStage: {
    artifact: 'status.preflightStage.artifact',
    render: 'status.preflightStage.render',
    cluster: 'status.preflightStage.cluster',
    runtime_pull: 'status.preflightStage.runtime_pull',
  },
  // api/proto/common/v1/domain.proto: BundleStatus. The archive restore response returns
  // the status a bundle was archived FROM (`previous_status`), so this is display copy.
  bundle: {
    received: 'status.bundle.received',
    validated: 'status.bundle.validated',
    rejected: 'status.bundle.rejected',
    archived: 'status.bundle.archived',
    unspecified: 'status.bundle.unspecified',
  },
  // TimelineEntry.ack_stage on an ACK entry. The operator reports `persisted` once the
  // command is fsynced locally; the console shows that as a stage note, never the wire
  // word itself (unknown values fall back to the neutral label in the component).
  ackStage: {
    persisted: 'operation.timeline.ackStage.persisted',
    unknown: 'operation.timeline.ackStage.unknown',
  },
};

/**
 * Chinese label for a server value.
 *
 * An unknown value falls back to the raw value on purpose: a new server status must stay
 * visible (and obviously untranslated) rather than render as an empty cell.
 */
export function statusLabel(domain: StatusDomain, value: string | null | undefined): string {
  if (!value) return '';
  const key = LABELS[domain][value];
  return key ? t(key) : value;
}
