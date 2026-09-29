import { Code, ConnectError } from '@connectrpc/connect';
import { correlationLine, copyForReason, describeError } from './error-copy';
import { create } from '@bufbuild/protobuf';
import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt';
import {
  EmergencyAction,
  ListStuckLocksRequestSchema,
  ReleaseEmergencyLockRequestSchema,
  ReleaseMode,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { orchestratorClient } from './client';

/*
 * Emergency stuck locks (REQ-087, A10 of the UX plan's missing surfaces).
 *
 * A stuck lock is a terminal EMERGENCY whose cluster effect is still UNKNOWN past
 * the observation window; it keeps the definition from accepting new work. The
 * contract that shapes this page:
 *  - ListStuckLocks is read-only, scoped to the caller's organization, and takes an
 *    OPTIONAL release definition id (empty = everything stuck in scope);
 *  - ReleaseEmergencyLock is NOT replayable and has NO UNDO: the lock must still be
 *    stuck (a second call reports NOT_FOUND or a state conflict), and releasing
 *    wrongly reopens a definition whose cluster state is still unknown;
 *  - `reason` is required (1-1000 characters) and `mode` must be chosen:
 *    NOT_APPLIED_PROVEN records the effect as NOT_APPLIED, AUDITED_OVERRIDE leaves it
 *    UNKNOWN while taking over the target and is fully audited.
 */

export interface StuckLockView {
  intentId: string;
  operationId: string;
  releaseDefinitionId: string;
  action: string;
  lockPathSummary: string;
  terminalAt: string | null;
  stuckSince: string | null;
  observeTimeoutDisplay: string;
}

export interface ReleaseLockInput {
  intentId: string;
  reason: string;
  mode: 'NOT_APPLIED_PROVEN' | 'AUDITED_OVERRIDE';
  evidence?: string;
}

export interface ReleaseLockResult {
  intentId: string;
  lockReleased: boolean;
  effectStatus: string;
  auditEventId: string;
}

const ACTION_LABELS: Record<number, string> = {
  [EmergencyAction.SET_CONTAINER_IMAGE]: '设置容器镜像',
  [EmergencyAction.SET_REPLICAS]: '设置副本数',
  [EmergencyAction.SET_APPROVED_ANNOTATION]: '设置批准注解',
};

function toIso(value: Timestamp | undefined): string | null {
  return value ? timestampDate(value).toISOString() : null;
}

export function actionLabel(action: EmergencyAction): string {
  return ACTION_LABELS[action] ?? '未知动作';
}

export async function listStuckLocks(releaseDefinitionId = ''): Promise<StuckLockView[]> {
  const response = await orchestratorClient.listStuckLocks(
    create(ListStuckLocksRequestSchema, { releaseDefinitionId }),
  );
  return response.locks.map((lock) => ({
    intentId: lock.intentId,
    operationId: lock.operationId,
    releaseDefinitionId: lock.releaseDefinitionId,
    action: actionLabel(lock.action),
    lockPathSummary: lock.lockPathSummary,
    terminalAt: toIso(lock.terminalAt),
    stuckSince: toIso(lock.stuckSince),
    observeTimeoutDisplay: lock.observeTimeoutDisplay,
  }));
}

export async function releaseEmergencyLock(input: ReleaseLockInput): Promise<ReleaseLockResult> {
  const response = await orchestratorClient.releaseEmergencyLock(
    create(ReleaseEmergencyLockRequestSchema, {
      intentId: input.intentId,
      reason: input.reason,
      mode: input.mode === 'NOT_APPLIED_PROVEN' ? ReleaseMode.NOT_APPLIED_PROVEN : ReleaseMode.AUDITED_OVERRIDE,
      evidence: input.evidence ?? '',
    }),
  );
  return {
    intentId: response.intentId,
    lockReleased: response.lockReleased,
    effectStatus: response.effectStatus === 1 ? 'NOT_APPLIED' : response.effectStatus === 2 ? 'APPLIED' : 'UNKNOWN',
    auditEventId: response.auditEventId,
  };
}

export type StuckLockFailureCode =
  | 'definition_not_found'
  | 'reason_required'
  | 'mode_required'
  | 'not_found'
  | 'permission_denied'
  | 'session_expired'
  | 'conflict'
  | 'unavailable';

export interface StuckLockFailure {
  /** Stable correlation data for the technical-details line (never payload data). */
  details: string;
  code: StuckLockFailureCode;
  message: string;
  /** True when the code came from the stable X-Reason-Code header. */
  typed: boolean;
}

const MESSAGES: Record<StuckLockFailureCode, string> = {
  // ReleaseEmergencyLock reaches definition_not_found too (it re-authorizes the
  // lock's definition at emergency_stuck.go:157), which is NOT "the lock is gone".
  definition_not_found: '该锁所属的 Release Definition 不存在，或不在你的授权范围内',
  reason_required: '必须填写释放原因（1-1000 字），evidence 不超过 500 字',
  mode_required: '必须选择释放模式：NOT_APPLIED_PROVEN 或 AUDITED_OVERRIDE',
  not_found: '未找到该锁：它可能已被释放（释放不可重放）',
  permission_denied: '无权释放锁：需要管理员角色与 release.emergency.execute 能力',
  session_expired: '会话无法解析，请重新登录',
  conflict: '该锁已不再处于卡住状态，请刷新后确认',
  unavailable: '释放失败，请稍后重试',
};

/*
 * Reads the stable carriers first — X-Reason-Code (emergencyError sets it) and then
 * the Connect code — never a free-text substring.
 */
export function mapStuckLockError(error: unknown): StuckLockFailure {
  // The shared layer owns the copy for stable reason codes; the local function
  // keeps this feature's code classification (which drives reload/retry).
  return withSharedCopy(mapStuckLockErrorLocal(error), error);
}

function mapStuckLockErrorLocal(error: unknown): StuckLockFailure {
  const connectError = ConnectError.from(error);
  const reason = (connectError.metadata.get('X-Reason-Code') ?? '').toLowerCase();
  const pick = (code: StuckLockFailureCode, typed = false): StuckLockFailure => ({
    code,
    message: MESSAGES[code],
    typed,
    details: '',
  });

  if (reason === 'reason_required') return pick('reason_required', true);
  if (reason === 'release_mode_unspecified') return pick('mode_required', true);
  if (reason === 'intent_not_found') return pick('not_found', true);
  if (reason === 'definition_not_found') return pick('definition_not_found', true);
  if (reason === 'authentication_required') return pick('session_expired', true);

  if (connectError.code === Code.InvalidArgument) return pick('reason_required');
  if (connectError.code === Code.NotFound) return pick('not_found');
  if (connectError.code === Code.PermissionDenied) return pick('permission_denied');
  if (connectError.code === Code.Unauthenticated) return pick('session_expired');
  if (connectError.code === Code.FailedPrecondition || connectError.code === Code.Aborted) return pick('conflict', true);
  return pick('unavailable');
}

/** Centralised copy for a known reason code overrides the local fallback wording. */
function withSharedCopy(failure: StuckLockFailure, error: unknown): StuckLockFailure {
  const described = describeError(error);
  const shared = described.message;
  return {
    ...failure,
    message: shared ?? failure.message,
    details: correlationLine(described),
  };
}

/**
 * The LIST path answers NOT_FOUND for an unknown release definition, which is a
 * filter problem — reporting it as "the lock is gone" would tell the operator their
 * lock had been released (found by the dev-env E2E of TASK-195).
 */
export function mapStuckLockListError(error: unknown): StuckLockFailure {
  const connectError = ConnectError.from(error);
  const reason = (connectError.metadata.get('X-Reason-Code') ?? '').toLowerCase();
  // The list path answers definition_not_found with the header set, so keep the
  // typed flag honest instead of losing it to the code fallback.
  if (reason === 'definition_not_found' || connectError.code === Code.NotFound) {
    return {
      code: 'definition_not_found',
      // The copy lives in the catalog (W5); this path is the one the UI actually hits.
      message: copyForReason('definition_not_found_filter') ?? '',
      typed: reason === 'definition_not_found',
      details: correlationLine(describeError(error)),
    };
  }
  return mapStuckLockError(error);
}
