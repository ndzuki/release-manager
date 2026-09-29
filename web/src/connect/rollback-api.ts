import { Code, ConnectError } from '@connectrpc/connect';
import { correlationLine, describeError } from './error-copy';
import { create } from '@bufbuild/protobuf';
import { CreateOperationGateDetailSchema, RollbackReleaseRequestSchema } from '@/gen/orchestrator/v1/orchestrator_pb';
import { orchestratorClient } from './client';

/*
 * Rollback of a release definition to a previous Helm revision (REQ-056 AC-056-08,
 * A11 of the UX plan's missing surfaces).
 *
 * The console had no rollback surface at all, although the RPC is implemented
 * (internal/orchestrator/rollback.go) and wired. What the server enforces, and the
 * client therefore mirrors before spending a request:
 *  - `reason` is REQUIRED (`INVALID_ARGUMENT: reason is required for rollback`);
 *  - `target_revision >= 1` and `expected_current_revision >= 1`;
 *  - `target_revision < expected_current_revision` (a rollback goes backwards) —
 *    `INVALID_ARGUMENT: target_revision X must be < expected_current_revision Y`;
 *  - the deprecated `values_revision_id` / `values_patch` fields are REJECTED
 *    (`rollback_values_not_allowed`), so this client never sends them;
 *  - the Idempotency-Key header is mandatory; replaying the same key with the same
 *    request returns the SAME operation, and a different request under one key is
 *    refused with ALREADY_EXISTS.
 */
export interface RollbackInput {
  releaseDefinitionId: string;
  targetRevision: number;
  expectedCurrentRevision: number;
  reason: string;
  idempotencyKey: string;
}

export interface RollbackResult {
  operationId: string;
  fromRevision: number;
  toRevision: number;
  state: string;
}

export async function rollbackRelease(input: RollbackInput): Promise<RollbackResult> {
  const response = await orchestratorClient.rollbackRelease(
    create(RollbackReleaseRequestSchema, {
      releaseDefinitionId: input.releaseDefinitionId,
      targetRevision: input.targetRevision,
      expectedCurrentRevision: input.expectedCurrentRevision,
      reason: input.reason,
    }),
    { headers: new Headers({ 'Idempotency-Key': input.idempotencyKey }) },
  );

  return {
    operationId: response.operationId,
    fromRevision: response.fromRevision,
    toRevision: response.toRevision,
    state: response.state,
  };
}

export type RollbackFailureCode =
  | 'reason_required'
  | 'revision_invalid'
  | 'stale_revision'
  | 'convergence_pending'
  | 'emergency_unresolved'
  | 'release_busy'
  | 'definition_disabled'
  | 'release_not_found'
  | 'idempotency_conflict'
  | 'not_found'
  | 'permission_denied'
  | 'unavailable';

export interface RollbackFailure {
  /** Stable correlation data for the technical-details line (never payload data). */
  details: string;
  code: RollbackFailureCode;
  message: string;
  retryable: boolean;
  /** True when the code came from the typed gate detail or X-Reason-Code. */
  typed: boolean;
}

const MESSAGES: Record<RollbackFailureCode, string> = {
  reason_required: '回滚必须填写原因',
  revision_invalid: '目标 Revision 不合法：必须是 1 以上且小于当前 Revision',
  stale_revision: '集群报告的 Revision 与提交时不一致（视图过期），请刷新发布清单后重试',
  convergence_pending: '存在待收敛任务，无法回滚；请先处理收敛任务',
  emergency_unresolved: '存在未解析的紧急变更效果，无法回滚；请先处置紧急变更',
  release_busy: '该 Release 有正在进行的操作，请等它结束后再回滚',
  definition_disabled: '该 Release Definition 已停用，无法回滚',
  release_not_found: '集群里没有该 Release 的已安装版本（可能未成功安装过）',
  idempotency_conflict: '同样的幂等键已用于不同的请求，请重新发起',
  not_found: '未找到该 Release Definition',
  permission_denied: '无权回滚：需要 release/write 与 deployer 或管理员能力',
  unavailable: '回滚失败，请稍后重试',
};

/*
 * The server expresses its FAILED_PRECONDITION causes as a stable machine token
 * followed by ": detail" (`revision_conflict: expected revision N, but current...`).
 * We match the LEADING TOKEN, not a free-text substring: the repo convention
 * (web/src/features/emergency/errors.ts) is typed detail -> X-Reason-Code -> code,
 * and free text is only a last resort for causes that carry no other carrier.
 */
const PRECONDITION_TOKENS: Array<[string, RollbackFailureCode]> = [
  ['revision_conflict', 'stale_revision'],
  ['release_convergence_pending', 'convergence_pending'],
  ['emergency_effect_unresolved', 'emergency_unresolved'],
  ['release_busy', 'release_busy'],
  ['release_definition_disabled', 'definition_disabled'],
  ['release_not_found', 'release_not_found'],
];

/** Documented machine prefixes of rollback's INVALID_ARGUMENT refusals. */
const INVALID_INPUT_PREFIXES: Array<[string, RollbackFailureCode]> = [
  ['reason', 'reason_required'],
  ['target_revision', 'revision_invalid'],
  ['expected_current_revision', 'revision_invalid'],
  ['rollback_values', 'revision_invalid'],
  ['idempotency_key', 'unavailable'],
];

export function mapRollbackError(error: unknown): RollbackFailure {
  // The shared layer owns the copy for stable reason codes; the local function
  // keeps this feature's code classification (which drives reload/retry).
  return withSharedCopy(mapRollbackErrorLocal(error), error);
}

function mapRollbackErrorLocal(error: unknown): RollbackFailure {
  const connectError = ConnectError.from(error);
  const reason = (connectError.metadata.get('X-Reason-Code') ?? '').toLowerCase();
  const raw = (connectError.rawMessage ?? connectError.message ?? '').trim();
  const token = raw.split(':', 1)[0]!.trim().toLowerCase();
  const pick = (code: RollbackFailureCode, typed = false, retryable = false): RollbackFailure => ({
    code,
    message: MESSAGES[code],
    retryable,
    details: '',
    typed,
  });

  // 1) Typed gate detail: the only carrier that says WHICH gate refused.
  const gate = connectError.findDetails(CreateOperationGateDetailSchema)[0];
  if (gate) {
    if (gate.convergenceTaskIds.length > 0) return pick('convergence_pending', true);
    if (gate.unresolvedOperationIds.length > 0) return pick('emergency_unresolved', true);
  }

  // 2) Stable reason-code metadata.
  if (reason === 'release_busy') return pick('release_busy', true);
  if (reason.includes('convergence')) return pick('convergence_pending', true);

  if (connectError.code === Code.FailedPrecondition) {
    const matched = PRECONDITION_TOKENS.find(([candidate]) => token === candidate);
    if (matched) {
      // Only a moved revision is fixed by refreshing and retrying; a disabled
      // definition, an unresolved emergency effect and a busy release are not
      // (mapOperationError makes the same call for release_busy).
      const retryable = matched[1] === 'stale_revision';
      return pick(matched[1], false, retryable);
    }
    return pick('unavailable', false, true);
  }

  if (connectError.code === Code.AlreadyExists) return pick('idempotency_conflict');
  if (connectError.code === Code.NotFound) return pick('not_found');
  if (connectError.code === Code.PermissionDenied) return pick('permission_denied');
  if (connectError.code === Code.InvalidArgument) {
    // rollback.go writes its refusals as bare phrases (`reason is required for
    // rollback`) with no reason code, so match the DOCUMENTED machine prefix rather
    // than a substring anywhere in the message.
    const prefix = INVALID_INPUT_PREFIXES.find(([candidate]) => raw.toLowerCase().startsWith(candidate));
    if (prefix) return pick(prefix[1], false, prefix[1] === 'unavailable');
    return pick('revision_invalid');
  }
  return pick('unavailable', false, true);
}

/** Centralised copy for a known reason code overrides the local fallback wording. */
function withSharedCopy(failure: RollbackFailure, error: unknown): RollbackFailure {
  const described = describeError(error);
  const shared = described.message;
  return {
    ...failure,
    message: shared ?? failure.message,
    details: correlationLine(described),
  };
}
