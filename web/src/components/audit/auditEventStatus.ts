import { t, type MessageKey } from '@/i18n/messages';

/*
 * Audit outcome → StatusTag tone, and the Chinese label for the outcome.
 *
 * `AuditEvent.status` is a FREE-FORM string on the wire (api/proto/audit/v1/audit.proto
 * documents resource_type/action/status as open sets, D-62). The values below are the
 * ones the server actually writes today, each pinned to the producer:
 *
 *   - "success"   internal/audit/audit_service_handler.go:239 (export request receipt)
 *   - "succeeded" internal/orchestrator/audit.go:17 and internal/store/store.go:158
 *   - "failed"    internal/orchestrator/audit.go:19 and internal/store/store.go:159
 *   - "trusted", "rejected", "policy_warning", "signature_missing",
 *     "verification_unavailable"  internal/store/store.go:21-25 (VerificationStatus),
 *     written verbatim as the audit status by internal/orchestrator/service.go:1755
 *   - "blocked", "would_block" (or "")  internal/orchestrator/admission.go:74-105,
 *     written as the audit status at internal/orchestrator/admission.go:281
 *   - the operation states pending/preflight/queued/running/cancelling/cancelled/timeout
 *     internal/store/store.go:153-161, copied into the audit status by
 *     internal/orchestrator/emergency.go:932-935 (status = string(op.Status))
 *   - "accepted"  accepted by the audit filter list (web/src/stores/audit.ts:62) and
 *     persisted as-is by the emitter (internal/audit/emitter.go records the event it
 *     was given)
 *
 * The tone rule is deliberately coarse because the column answers one question —
 * "did it work?". Success outcomes are `ok`; a refusal or a failure is `er`; everything
 * else (in-flight, shadow-mode, warning-only, unknown server value) is `neutral` so the
 * row keeps its raw value visible without claiming an outcome it does not have.
 *
 * The label falls back to the raw server value: a new status must stay visible (and
 * obviously untranslated) rather than render as an empty cell, which is the same rule
 * statusLabel() follows in src/i18n/status-labels.ts.
 */

/** StatusTag's tone contract is narrower than this module needs to expose. */
export type AuditStatusTone = 'ok' | 'er' | 'neutral';

const OK_STATUSES = new Set(['success', 'succeeded', 'trusted']);

// `blocked` is an admission refusal (admission.go:91/93) and `rejected` is the trust
// verification refusal (store.go:23): both are the "被拒" bucket the page asks for.
const REFUSED_STATUSES = new Set(['failed', 'rejected', 'blocked']);

const LABEL_KEYS: Record<string, MessageKey> = {
  success: 'status.audit.success',
  succeeded: 'status.audit.succeeded',
  failed: 'status.audit.failed',
  accepted: 'status.audit.accepted',
  trusted: 'audit.result.trusted',
  rejected: 'audit.result.rejected',
  blocked: 'audit.result.blocked',
  would_block: 'audit.result.wouldBlock',
  policy_warning: 'audit.result.policyWarning',
  signature_missing: 'audit.result.signatureMissing',
  verification_unavailable: 'audit.result.verificationUnavailable',
  pending: 'audit.result.pending',
  preflight: 'audit.result.preflight',
  queued: 'audit.result.queued',
  running: 'audit.result.running',
  cancelling: 'audit.result.cancelling',
  cancelled: 'audit.result.cancelled',
  timeout: 'audit.result.timeout',
};

export function auditStatusTone(status: string): AuditStatusTone {
  if (OK_STATUSES.has(status)) return 'ok';
  if (REFUSED_STATUSES.has(status)) return 'er';
  return 'neutral';
}

export function auditStatusLabel(status: string): string {
  if (!status) return '';
  const key = LABEL_KEYS[status];
  return key ? t(key) : status;
}
