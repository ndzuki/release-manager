import { create, type JsonObject } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError, type Client } from '@connectrpc/connect';
import {
  BundleService,
  CancelOperationRequestSchema,
  CreateOperationRequestSchema,
  GetOperationRequestSchema,
  ListNonTerminalOperationsRequestSchema,
  ListOperationsRequestSchema,
  ListBundlesRequestSchema,
  OrchestratorService,
  RollbackReleaseRequestSchema,
  WatchOperationRequestSchema,
  type WatchOperationResponse,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { BundleStatus } from '@/gen/common/v1/domain_pb';
import { PaginationSchema } from '@/gen/common/v1/types_pb';
import { bundleClient, orchestratorClient } from '@/connect/client';
import {
  mapBundleSummary,
  mapOperation,
  type Operation,
  type OperationOptions,
  type OperationState,
  type OperationType,
  type PatchOverride,
  type PreflightResult,
} from '@/types/operation';

export interface CreateOperationInput {
  idempotencyKey: string;
  releaseDefinitionId: string;
  operationType: OperationType;
  bundleId?: string;
  expectedCurrentRevision?: number;
  valuesRevisionId: string;
  patch: PatchOverride[];
}

export interface RollbackReleaseInput {
  idempotencyKey: string;
  releaseDefinitionId: string;
  targetRevision: number;
  expectedCurrentRevision: number;
  reason: string;
}

export interface RolledBackOperation {
  operationId: string;
  fromRevision: number;
  toRevision: number;
  state: string;
}

export interface CreatedOperation {
  operationId: string;
  state: OperationState;
  preflightId: string;
  acceptedAt: string | null;
}

export interface OperationAPIError {
  code: string;
  message: string;
  operationId: string | null;
  retryable: boolean;
  /** cursor_expired: authoritative snapshot sequence carried by the error. */
  snapshotSequence?: bigint;
  /** cursor_expired: retention boundary carried by the error. */
  retainedFromSequence?: bigint;
  /** cursor_expired: base64 protojson OperationSnapshot so the client can rebuild the stream. */
  snapshotProto?: string;
}

export interface CancelOperationInput {
  operationId: string;
  reason: string;
  expectedStateVersion: bigint;
  idempotencyKey: string;
}

export interface CancelOperationResult {
  operation: Operation;
  requestId: string;
}

let operationClient: Client<typeof OrchestratorService> = orchestratorClient;
let bundleServiceClient: Client<typeof BundleService> = bundleClient;

export function setOperationClientForTest(
  nextClient: Client<typeof OrchestratorService>,
  nextBundleClient?: Client<typeof BundleService>,
): void {
  operationClient = nextClient;
  if (nextBundleClient) bundleServiceClient = nextBundleClient;
}

export async function loadOperationOptions(releaseDefinitionId: string): Promise<OperationOptions> {
  const bundles = await bundleServiceClient.listBundles(create(ListBundlesRequestSchema, {
    releaseDefinitionId,
    statusFilter: [BundleStatus.VALIDATED],
    pagination: create(PaginationSchema, { pageSize: 100 }),
  }));

  return {
    bundles: bundles.bundles.map(mapBundleSummary),
  };
}

function buildValuesPatch(patch: PatchOverride[]): string {
  if (patch.length === 0) return '{}';
  const mergePatch: Record<string, string> = {};
  for (const override of patch) {
    mergePatch[override.path] = override.value;
  }
  return JSON.stringify(mergePatch);
}

export async function createOperation(input: CreateOperationInput): Promise<CreatedOperation> {
  const response = await operationClient.createOperation(
    create(CreateOperationRequestSchema, {
      operationType: input.operationType,
      bundleId: input.bundleId ?? '',
      releaseDefinitionId: input.releaseDefinitionId,
      valuesRevisionId: input.valuesRevisionId,
      valuesPatch: JSON.parse(buildValuesPatch(input.patch)) as JsonObject,
      expectedCurrentRevision: input.expectedCurrentRevision ?? 0,
    }),
    { headers: new Headers({ 'Idempotency-Key': input.idempotencyKey }) },
  );

  return {
    operationId: response.operationId,
    state: response.state.toLowerCase() as OperationState,
    preflightId: response.preflightId,
    acceptedAt: response.acceptedAt ? timestampDate(response.acceptedAt).toISOString() : null,
  };
}
/**
 * One release definition's operation history, newest first (REQ-056).
 *
 * Keyset-paginated on (created_at, id): pass the previous response's `nextCursor`
 * to continue. Implemented server-side since TASK-095
 * (internal/orchestrator/operations_query.go); the proto comment that claimed it
 * always answers UNIMPLEMENTED was corrected in TASK-184.
 *
 * Page size is NOT the generic 50 from connect-surface: `contracts.NormalizePageSize`
 * defaults to 20 and caps at 100.
 */
const OPERATION_STATES: OperationState[] = [
  'pending', 'preflight', 'queued', 'running', 'cancelling', 'succeeded', 'failed', 'cancelled', 'timeout',
];

/** The summary carries the STORE status string ("succeeded"), not the proto enum. */
function summaryState(raw: string): OperationState {
  const value = (raw ?? '').trim().toLowerCase();
  return (OPERATION_STATES as string[]).includes(value) ? (value as OperationState) : 'pending';
}

/**
 * The summary carries the STORE operation type ("INSTALL", internal/store/store.go:130-133),
 * not the proto enum. Anything unrecognised is read as INSTALL, matching the CreateOperation
 * contract where INSTALL is the only type carrying no extra constraint.
 */
function operationTypeOf(raw: string): OperationType {
  return raw === 'UPGRADE' || raw === 'ROLLBACK' || raw === 'EMERGENCY' ? raw : 'INSTALL';
}


// RollbackRelease is a standalone RPC, not a CreateOperation variant: CreateOperation accepts
// INSTALL/UPGRADE only, and the canonical rollback request carries target_revision. The
// deprecated values_revision_id/values_patch fields are deliberately never set here because
// the server rejects them (rollback_values_not_allowed, AC-067-16).
export async function rollbackRelease(input: RollbackReleaseInput): Promise<RolledBackOperation> {
  const response = await operationClient.rollbackRelease(
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
export interface OperationSummaryItem {
  operationId: string;
  operationType: OperationType;
  state: OperationState;
  revision: number;
  createdAt: string | null;
}

export interface OperationHistoryPage {
  operations: OperationSummaryItem[];
  nextCursor: string;
}

export async function listOperations(
  releaseDefinitionId: string,
  options: { statusFilter?: string; limit?: number; cursor?: string } = {},
): Promise<OperationHistoryPage> {
  const response = await operationClient.listOperations(create(ListOperationsRequestSchema, {
    releaseDefinitionId,
    statusFilter: options.statusFilter ?? '',
    limit: options.limit ?? 20,
    cursor: options.cursor ?? '',
  }));
  return {
    operations: response.operations.map((item) => ({
      operationId: item.operationId,
      operationType: operationTypeOf(item.operationType),
      state: summaryState(item.state),
      revision: item.revision,
      createdAt: item.createdAt ? timestampDate(item.createdAt).toISOString() : null,
    })),
    nextCursor: response.nextCursor,
  };
}

/**
 * One row of the cross-release non-terminal feed (REQ-100 handoff H9 / TASK-276).
 *
 * The definition and customer identity travel INLINE on purpose, so rendering a page
 * costs one store read instead of a name lookup per row, and `state`/`operationType`
 * arrive as the STORE strings — normalised through the same two helpers as
 * OperationSummaryItem so one server value reads the same on both lists.
 *
 * A `type`, not an `interface`, on purpose: DataTable takes `Record<string, unknown>[]`
 * rows and TypeScript only gives object-literal aliases an implicit index signature.
 */
export type NonTerminalOperationItem = {
  operationId: string;
  operationType: OperationType;
  state: OperationState;
  releaseDefinitionId: string;
  releaseDefinitionName: string;
  customerId: string;
  customerName: string;
  /**
   * Cluster the operation's release definition targets (TASK-279). The detail
   * page's canonical route is release-scoped, so this is what lets the centre
   * hand it the real cluster context instead of the scope-less fallback.
   */
  clusterId: string;
  createdAt: string | null;
  updatedAt: string | null;
  emergency: boolean;
  revision: number;
};

export interface NonTerminalOperationPage {
  operations: NonTerminalOperationItem[];
  /** Empty on the last page; echo it back as `pageToken` to continue. */
  nextPageToken: string;
}

export interface NonTerminalOperationQuery {
  /**
   * 0/unset selects the server default (20); the server clamps above 100 and rejects a
   * negative value with `invalid_page_size`. Deliberately passed through unclamped so
   * the wire assertion can prove the contract is the server's, not a client guess.
   */
  pageSize?: number;
  /** Opaque (created_at, id) cursor from the previous response's nextPageToken. */
  pageToken?: string;
  /** Optional narrowing; a customer outside the caller's active bindings is denied. */
  customerId?: string;
}

/**
 * Cross-release, non-terminal operations, oldest wait first (TASK-276).
 *
 * Scope is the caller's organization's ACTIVE bindings: rows outside it are simply
 * absent, while an explicit out-of-scope `customerId` answers PERMISSION_DENIED. The
 * order is `created_at ASC, id ASC` and is owned by the server, so this wrapper never
 * re-sorts and never invents a total: `nextPageToken` is the only end-of-list signal.
 */
export async function listNonTerminalOperations(
  query: NonTerminalOperationQuery = {},
): Promise<NonTerminalOperationPage> {
  const response = await operationClient.listNonTerminalOperations(create(ListNonTerminalOperationsRequestSchema, {
    pageSize: query.pageSize ?? 0,
    pageToken: query.pageToken ?? '',
    customerId: query.customerId ?? '',
  }));
  return {
    operations: response.operations.map((item) => ({
      operationId: item.operationId,
      operationType: operationTypeOf(item.operationType),
      state: summaryState(item.state),
      releaseDefinitionId: item.releaseDefinitionId,
      releaseDefinitionName: item.releaseDefinitionName,
      customerId: item.customerId,
      customerName: item.customerName,
      clusterId: item.clusterId,
      createdAt: item.createdAt ? timestampDate(item.createdAt).toISOString() : null,
      updatedAt: item.updatedAt ? timestampDate(item.updatedAt).toISOString() : null,
      emergency: item.emergency,
      revision: item.revision,
    })),
    nextPageToken: response.nextPageToken,
  };
}

export async function getOperation(operationId: string): Promise<Operation> {
  const response = await operationClient.getOperation(create(GetOperationRequestSchema, { operationId }));
  if (!response.operation) throw new ConnectError('operation response is empty', Code.Internal);
  return mapOperation(response.operation);
}

/**
 * Reads the preflight stage results (TASK-149 / REQ-056 AC-056-03). Returns null
 * while preflight is still in flight, so the caller renders nothing rather than
 * an empty panel.
 */
export async function getPreflightResult(operationId: string): Promise<PreflightResult | null> {
  const response = await operationClient.getOperation(create(GetOperationRequestSchema, { operationId }));
  const result = response.preflightResult;
  if (!result) return null;
  return {
    overall: result.overall,
    failedStage: result.failedStage,
    errorCode: result.errorCode,
    stages: result.stages.map((stage) => ({ stage: stage.stage, status: stage.status, detail: stage.detail })),
  };
}

/**
 * Opens the WatchOperation server stream. The caller owns the returned
 * AsyncIterable and must pass an AbortSignal to cancel it on teardown.
 */
export async function watchOperation(
  operationId: string,
  afterSequence: bigint,
  signal: AbortSignal,
): Promise<AsyncIterable<WatchOperationResponse>> {
  return operationClient.watchOperation(
    create(WatchOperationRequestSchema, { operationId, afterSequence }),
    { signal },
  );
}

export async function cancelOperation(input: CancelOperationInput): Promise<CancelOperationResult> {
  const response = await operationClient.cancelOperation(
    create(CancelOperationRequestSchema, {
      operationId: input.operationId,
      reason: input.reason,
      expectedStateVersion: input.expectedStateVersion,
    }),
    { headers: new Headers({ 'Idempotency-Key': input.idempotencyKey }) },
  );
  if (!response.operation) throw new ConnectError('cancel response is empty', Code.Internal);
  return { operation: mapOperation(response.operation), requestId: response.requestId };
}

export function mapOperationError(error: unknown): OperationAPIError {
  const connectError = ConnectError.from(error);
  const reason = connectError.metadata.get('X-Reason-Code') ?? '';
  const operationId = connectError.metadata.get('X-Operation-ID');
  const messages: Record<string, string> = {
    release_busy: 'Release 有进行中的操作',
    revision_conflict: 'Revision 已被更新，请刷新后重试',
    bundle_untrusted: '所选制品未通过验证',
    idempotency_conflict: '相同幂等键已用于其他请求',
    values_not_approved: '所选配置版本未审批',
    non_bundle_image: 'Patch 引用了 Bundle 外镜像',
    secret_literal_forbidden: 'Secret 类字段必须使用 Secret 引用',
    permission_denied: '无权执行该操作',
    target_revision_not_found: '目标 Revision 不存在，请刷新后重试',
    rollback_values_not_allowed: '回滚请求不能携带 values 覆盖',
    invalid_argument: '请求参数不合法，请检查后重试',
    not_found: '操作不存在或当前账号不可见',
    cancel_not_allowed: '当前状态不允许取消',
    optimistic_lock_conflict: '操作状态已变更，正在刷新最新状态',
    cursor_expired: '历史事件已超出服务端保留窗口',
    invalid_cursor: '页码已过期，已重新从第一页加载',
    stream_disconnected: '实时连接已断开',
    rollout_timeout: '发布超时，请检查集群状态',
    dependency_unavailable: '服务暂时不可用，请稍后重试',
  };

  const stableCode = reason && messages[reason] ? reason : '';
  if (stableCode) {
    return {
      code: stableCode,
      message: messages[stableCode],
      operationId,
      retryable:
        stableCode === 'optimistic_lock_conflict' ||
        stableCode === 'cursor_expired' ||
        stableCode === 'invalid_cursor' ||
        stableCode === 'stream_disconnected' ||
        stableCode === 'dependency_unavailable',
      snapshotSequence: parseBigIntHeader(connectError.metadata.get('X-Snapshot-Sequence')),
      retainedFromSequence: parseBigIntHeader(connectError.metadata.get('X-Retained-From-Sequence')),
      snapshotProto: connectError.metadata.get('X-Snapshot-Proto') || undefined,
    };
  }
  switch (connectError.code) {
    case Code.PermissionDenied:
      return { code: 'permission_denied', message: messages.permission_denied, operationId, retryable: false };
    case Code.NotFound:
      return { code: 'not_found', message: messages.not_found, operationId, retryable: false };
    case Code.InvalidArgument:
      return { code: 'invalid_argument', message: messages.invalid_argument, operationId, retryable: false };
    case Code.Aborted:
      return { code: 'optimistic_lock_conflict', message: messages.optimistic_lock_conflict, operationId, retryable: true };
    case Code.OutOfRange:
      return {
        code: 'cursor_expired',
        message: messages.cursor_expired,
        operationId,
        retryable: true,
        snapshotSequence: parseBigIntHeader(connectError.metadata.get('X-Snapshot-Sequence')),
        retainedFromSequence: parseBigIntHeader(connectError.metadata.get('X-Retained-From-Sequence')),
        snapshotProto: connectError.metadata.get('X-Snapshot-Proto') || undefined,
      };
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return { code: 'network_error', message: '网络错误，请检查连接后重试', operationId, retryable: true };
    default:
      return { code: reason || 'unknown', message: connectError.rawMessage || '操作请求失败', operationId, retryable: false };
  }
}

/**
 * True when the orchestrator refused the call because it is in MAINTENANCE MODE.
 *
 * `ListNonTerminalOperations` is deliberately NOT in orchestratorReadOnlyProcedures()
 * (cmd/orchestrator/main.go), so maintenance answers UNAVAILABLE with the raw message
 * `maintenance` — the same token CleanupService sends (see cleanup-api.ts). The token is
 * matched EXACTLY, never as a substring: a transport outage is also UNAVAILABLE and must
 * stay a retryable network error rather than be mislabelled as planned maintenance.
 *
 * This is a separate predicate rather than a new code inside mapOperationError because
 * the operation stream treats `network_error` as "reconnect" (stores/operationTimeline.ts);
 * maintenance must not silently change that behaviour on the write paths.
 */
export function isMaintenanceError(error: unknown): boolean {
  const connectError = ConnectError.from(error);
  const raw = (connectError.rawMessage ?? connectError.message ?? '').trim().toLowerCase();
  return connectError.code === Code.Unavailable && raw === 'maintenance';
}

function parseBigIntHeader(value: string | null | undefined): bigint | undefined {
  if (!value) return undefined;
  try {
    return BigInt(value);
  } catch {
    return undefined;
  }
}
