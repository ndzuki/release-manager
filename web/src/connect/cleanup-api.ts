import { Code, ConnectError } from '@connectrpc/connect';
import { create } from '@bufbuild/protobuf';
import { RunCleanupRequestSchema, UnarchiveBundleRequestSchema } from '@/gen/orchestrator/v1/cleanup_pb';
import { cleanupClient } from './client';
import { correlationLine, describeError } from './error-copy';

/*
 * Artifact lifecycle (REQ-069, A7 of the UX plan's missing surfaces).
 *
 * The console had no way to run retention GC or to undo an archived bundle, although
 * CleanupService serves both. What the contract makes the UI responsible for:
 *  - RunCleanup runs SYNCHRONOUSLY with a budget of up to about an hour, so the call
 *    needs a generous deadline (see LONG_UNARY_PROCEDURES in client.ts);
 *  - per-phase problems come back as a non-fatal `errors` list with a successful
 *    response, so "success" must be read together with that list — the counters are
 *    row counts, not freed bytes;
 *  - a repeated idempotency key inside the retention window (24h by default, but
 *    CONFIGURABLE) is REFUSED with ALREADY_EXISTS (`cleanup_already_requested`)
 *    rather than replaying the counters; a second call under that key WHILE the first
 *    is still running answers ALREADY_EXISTS `cleanup is already in progress`, and a
 *    conflicting run elsewhere answers RESOURCE_EXHAUSTED (`cleanup already running`)
 *    because concurrent replicas are serialised by a database lock. Single-node
 *    deployments may have NO key table at all, in which case only the in-process
 *    guard applies and the key-based refusal never happens;
 *  - both RPCs need cleanup/write, which only platform_admin holds, and are refused
 *    with UNAVAILABLE (`maintenance`) while the orchestrator is in maintenance mode.
 */

export interface CleanupResult {
  deletedBundles: number;
  deletedCandidates: number;
  deletedPreflights: number;
  skippedBundles: number;
  /** Per-phase, non-fatal problems reported alongside a successful run. */
  errors: string[];
}

export interface UnarchiveResult {
  bundleId: string;
  previousStatus: string;
}

export async function runCleanup(idempotencyKey: string): Promise<CleanupResult> {
  const response = await cleanupClient.runCleanup(create(RunCleanupRequestSchema, { idempotencyKey }));
  // The proto counters are int64 (bigint in TS) but they are ROW counts, far below
  // Number.MAX_SAFE_INTEGER, so narrowing them here keeps the display code simple.
  return {
    deletedBundles: Number(response.deletedBundles),
    deletedCandidates: Number(response.deletedCandidates),
    deletedPreflights: Number(response.deletedPreflights),
    skippedBundles: Number(response.skippedBundles),
    errors: [...response.errors],
  };
}

export async function unarchiveBundle(bundleId: string): Promise<UnarchiveResult> {
  const response = await cleanupClient.unarchiveBundle(create(UnarchiveBundleRequestSchema, { bundleId }));
  return { bundleId: response.bundleId, previousStatus: response.previousStatus };
}

export type CleanupFailureCode =
  | 'already_requested'
  | 'already_running'
  | 'maintenance'
  | 'not_found'
  | 'unrestorable'
  | 'invalid_input'
  | 'permission_denied'
  | 'unavailable';

export interface CleanupFailure {
  code: CleanupFailureCode;
  message: string;
  retryable: boolean;
  details: string;
}

const MESSAGES: Record<CleanupFailureCode, string> = {
  already_requested: '该幂等键在保留窗口内已请求过（默认 24 小时）；重复键不会重跑清理（请换一个新键）',
  already_running: '同一个键的清理仍在进行中，或另一个实例正在执行；请稍后再试',
  maintenance: '平台处于维护模式，清理与恢复被拒绝',
  not_found: '未找到该 Bundle',
  unrestorable: '该 Bundle 不可恢复（例如状态为已拒绝）',
  invalid_input: '输入不合法：幂等键需 1–64 个字符，或缺少 bundle id',
  permission_denied: '无权执行：清理/恢复只对 platform_admin 开放（cleanup/write）',
  unavailable: '操作失败，请稍后重试',
};

/**
 * CleanupService sets no X-Reason-Code, but its refusals are documented machine
 * tokens in the message itself (`cleanup_already_requested`), so those are matched
 * EXACTLY — never as free-text substrings.
 */
const EXACT_REASONS: Array<[string, CleanupFailureCode]> = [
  ['cleanup_already_requested', 'already_requested'],
  ['cleanup is already in progress', 'already_running'],
  ['cleanup already running', 'already_running'],
  ['maintenance', 'maintenance'],
  ['bundle_not_found', 'not_found'],
];

export function mapCleanupError(error: unknown): CleanupFailure {
  const connectError = ConnectError.from(error);
  const raw = (connectError.rawMessage ?? connectError.message ?? '').trim();
  const reason = (connectError.metadata.get('X-Reason-Code') ?? '').toLowerCase();
  const pick = (code: CleanupFailureCode, retryable = false): CleanupFailure => ({
    code,
    message: MESSAGES[code],
    retryable,
    details: correlationLine(describeError(error)),
  });

  const exact = EXACT_REASONS.find(([token]) => raw === token);
  if (exact) return pick(exact[1], exact[1] === 'already_running');
  if (reason === 'cleanup_already_requested') return pick('already_requested');

  if (connectError.code === Code.AlreadyExists) return pick('already_requested');
  if (connectError.code === Code.ResourceExhausted) return pick('already_running', true);
  if (connectError.code === Code.NotFound) return pick('not_found');
  if (connectError.code === Code.FailedPrecondition) return pick('unrestorable');
  if (connectError.code === Code.PermissionDenied) return pick('permission_denied');
  if (connectError.code === Code.InvalidArgument) return pick('invalid_input');
  if (connectError.code === Code.Unavailable && raw.toLowerCase() === 'maintenance') return pick('maintenance');
  return pick('unavailable', true);
}
