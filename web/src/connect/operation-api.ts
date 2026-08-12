import { create } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError, type Client } from '@connectrpc/connect';
import {
  BundleService,
  CreateOperationRequestSchema,
  GetOperationRequestSchema,
  ListBundlesRequestSchema,
  OrchestratorService,
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
  const response = await operationClient.createOperation(create(CreateOperationRequestSchema, {
    operationType: input.operationType,
    bundleId: input.bundleId ?? '',
    releaseDefinitionId: input.releaseDefinitionId,
    valuesRevisionId: input.valuesRevisionId,
    valuesPatch: buildValuesPatch(input.patch),
    expectedCurrentRevision: input.expectedCurrentRevision ?? 0,
    idempotencyKey: input.idempotencyKey,
  }));

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
    permission_denied: '无权创建操作',
  };

  if (reason && messages[reason]) {
    return { code: reason, message: messages[reason], operationId, retryable: false };
  }
  if (connectError.code === Code.PermissionDenied) {
    return { code: 'permission_denied', message: messages.permission_denied, operationId, retryable: false };
  }
  if (connectError.code === Code.Unavailable || connectError.code === Code.DeadlineExceeded) {
    return { code: 'network_error', message: '网络错误，请检查连接后重试', operationId, retryable: true };
  }
  return { code: reason || 'unknown', message: connectError.rawMessage || '操作请求失败', operationId, retryable: false };
}
