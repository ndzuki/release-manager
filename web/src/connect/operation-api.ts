import { create, type JsonObject } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError, type Client } from '@connectrpc/connect';
import {
  BundleService,
  CancelOperationRequestSchema,
  CreateOperationRequestSchema,
  GetOperationRequestSchema,
  ListBundlesRequestSchema,
  OrchestratorService,
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
export async function getOperation(operationId: string): Promise<Operation> {
  const response = await operationClient.getOperation(create(GetOperationRequestSchema, { operationId }));
  if (!response.operation) throw new ConnectError('operation response is empty', Code.Internal);
  return mapOperation(response.operation);
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
    invalid_argument: '请求参数不合法，请检查后重试',
    not_found: '操作不存在或当前账号不可见',
    cancel_not_allowed: '当前状态不允许取消',
    optimistic_lock_conflict: '操作状态已变更，正在刷新最新状态',
    cursor_expired: '历史事件已超出服务端保留窗口',
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

function parseBigIntHeader(value: string | null | undefined): bigint | undefined {
  if (!value) return undefined;
  try {
    return BigInt(value);
  } catch {
    return undefined;
  }
}
