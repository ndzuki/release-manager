import { Code, ConnectError, createClient } from '@connectrpc/connect';
import type { Client } from '@connectrpc/connect';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import {
  EnrollmentTokenState,
  OperatorErrorDetailSchema,
  OperatorLifecycleStatus,
  OperatorSessionStatus,
  OperatorSessionStatusReason,
  OrchestratorService,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import type {
  OperatorDetail as ProtoOperatorDetail,
  OperatorSummary as ProtoOperatorSummary,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { transport } from '@/connect/client';
import type {
  EnrollmentFormInput,
  EnrollmentTokenResult,
  OperatorApiError,
  OperatorDetail,
  OperatorDetailResult,
  OperatorLifecycleStatus as LifecycleStatus,
  OperatorListFilters,
  OperatorPage,
  OperatorRevocationResult,
  OperatorSessionStatus as SessionStatus,
  OperatorSessionStatusReason as SessionReason,
  PendingTokenMetadata,
} from '@/types/operator';

let client: Client<typeof OrchestratorService> = createClient(OrchestratorService, transport);

export function setOperatorClientForTest(replacement: Client<typeof OrchestratorService>): void {
  client = replacement;
}

function timestamp(value: Parameters<typeof timestampDate>[0] | undefined): string | null {
  return value ? timestampDate(value).toISOString() : null;
}

function mapLifecycle(value: OperatorLifecycleStatus): LifecycleStatus {
  if (value === OperatorLifecycleStatus.ACTIVE) return 'active';
  if (value === OperatorLifecycleStatus.SUPERSEDED) return 'superseded';
  if (value === OperatorLifecycleStatus.REVOKED) return 'revoked';
  return 'unknown';
}

function mapSession(value: OperatorSessionStatus): SessionStatus {
  if (value === OperatorSessionStatus.UNSPECIFIED) return null;
  if (value === OperatorSessionStatus.ONLINE) return 'online';
  if (value === OperatorSessionStatus.SUSPECT) return 'suspect';
  if (value === OperatorSessionStatus.OFFLINE) return 'offline';
  if (value === OperatorSessionStatus.REVOKED) return 'revoked';
  return 'unknown';
}

function mapReason(value: OperatorSessionStatusReason): SessionReason | null {
  const reasons: Partial<Record<OperatorSessionStatusReason, SessionReason>> = {
    [OperatorSessionStatusReason.NO_SESSION]: 'no_session',
    [OperatorSessionStatusReason.HEARTBEAT_TIMEOUT]: 'heartbeat_timeout',
    [OperatorSessionStatusReason.HEARTBEAT_DELAYED]: 'heartbeat_delayed',
    [OperatorSessionStatusReason.CERTIFICATE_REVOKED]: 'certificate_revoked',
    [OperatorSessionStatusReason.OPERATOR_SUPERSEDED]: 'operator_superseded',
    [OperatorSessionStatusReason.SESSION_REPLACED]: 'session_replaced',
    [OperatorSessionStatusReason.UNKNOWN]: 'unknown',
  };
  return reasons[value] ?? (value === OperatorSessionStatusReason.UNSPECIFIED ? null : 'unknown');
}

function mapSummary(value: ProtoOperatorSummary): OperatorPage['operators'][number] {
  return {
    id: value.id,
    name: value.name,
    customerId: value.customerId,
    clusterId: value.clusterId,
    clusterName: value.clusterName,
    lifecycleStatus: mapLifecycle(value.lifecycleStatus),
    sessionStatus: mapSession(value.sessionStatus),
    sessionStatusReason: mapReason(value.sessionStatusReason),
    lastHeartbeat: timestamp(value.lastHeartbeat),
    registeredAt: timestamp(value.registeredAt) ?? '',
    supersededAt: timestamp(value.supersededAt),
    revokedAt: timestamp(value.revokedAt),
  };
}

function requireDetail(value: ProtoOperatorDetail | undefined): OperatorDetail {
  if (!value?.summary) throw new ConnectError('operator response is missing', Code.Internal);
  return {
    ...mapSummary(value.summary),
    supersededBy: value.supersededBy || null,
    revokeReason: value.revokeReason || null,
    instanceId: value.instanceId || null,
    version: value.version || null,
    capabilities: { ...value.capabilities },
  };
}

function lifecycleToProto(value: LifecycleStatus | null): OperatorLifecycleStatus | undefined {
  if (value === 'active') return OperatorLifecycleStatus.ACTIVE;
  if (value === 'superseded') return OperatorLifecycleStatus.SUPERSEDED;
  if (value === 'revoked') return OperatorLifecycleStatus.REVOKED;
  return undefined;
}

function sessionToProto(value: SessionStatus | 'none'): OperatorSessionStatus | undefined {
  if (value === 'none') return OperatorSessionStatus.UNSPECIFIED;
  if (value === 'online') return OperatorSessionStatus.ONLINE;
  if (value === 'suspect') return OperatorSessionStatus.SUSPECT;
  if (value === 'offline') return OperatorSessionStatus.OFFLINE;
  if (value === 'revoked') return OperatorSessionStatus.REVOKED;
  return undefined;
}

export async function listOperators(
  customerId: string,
  clusterId: string,
  filters: OperatorListFilters,
  pageToken = '',
): Promise<OperatorPage> {
  const response = await client.listOperators({
    customerId,
    clusterId,
    lifecycleStatus: lifecycleToProto(filters.lifecycleStatus),
    sessionStatus: sessionToProto(filters.sessionStatus),
    pageSize: 20,
    pageToken,
  });
  return {
    operators: response.operators.map(mapSummary),
    nextPageToken: response.nextPageToken || null,
    totalCount: response.totalCount,
    heartbeatIntervalSeconds: response.heartbeatIntervalSeconds,
  };
}

export async function getOperator(customerId: string, clusterId: string, operatorId: string): Promise<OperatorDetailResult> {
  const response = await client.getOperator({ customerId, clusterId, operatorId });
  return { operator: requireDetail(response.operator), heartbeatIntervalSeconds: response.heartbeatIntervalSeconds };
}

export async function createEnrollmentToken(
  customerId: string,
  clusterId: string,
  input: EnrollmentFormInput,
  replacePendingToken = false,
): Promise<EnrollmentTokenResult> {
  const response = await client.createEnrollmentToken({
    customerId,
    clusterId,
    operatorName: input.operatorName.trim(),
    ttlMinutes: input.ttlMinutes,
    replacePendingToken,
  });
  return {
    token: response.token,
    expiresAt: timestamp(response.expiresAt) ?? '',
    customerId: response.customerId,
    clusterId: response.clusterId,
    clusterName: response.clusterName,
    operatorEndpoint: response.operatorEndpoint,
    installCommandTemplateVersion: response.installCommandTemplateVersion,
    installCommandTemplate: response.installCommandTemplate,
  };
}

export async function getEnrollmentTokenStatus(customerId: string, clusterId: string): Promise<PendingTokenMetadata> {
  const response = await client.getEnrollmentTokenStatus({ customerId, clusterId });
  const status = response.status;
  if (!status || status.state !== EnrollmentTokenState.PENDING) {
    return { state: 'none', createdAt: null, expiresAt: null, createdByDisplayName: null };
  }
  return {
    state: 'pending',
    createdAt: timestamp(status.createdAt),
    expiresAt: timestamp(status.expiresAt),
    createdByDisplayName: status.createdByDisplayName || null,
  };
}

export async function revokePendingEnrollmentToken(customerId: string, clusterId: string): Promise<boolean> {
  const response = await client.revokePendingEnrollmentToken({ customerId, clusterId });
  return response.changed;
}

export async function revokeOperator(customerId: string, clusterId: string, operatorId: string, reason: string): Promise<OperatorRevocationResult> {
  const response = await client.revokeOperator({ customerId, clusterId, operatorId, reason: reason.trim() });
  if (!response.operator) throw new ConnectError('operator response is missing', Code.Internal);
  return { operator: mapSummary(response.operator), changed: response.changed };
}

const ERROR_CODE_NAMES: Partial<Record<Code, string>> = {
  [Code.PermissionDenied]: 'permission_denied',
  [Code.NotFound]: 'not_found',
  [Code.AlreadyExists]: 'already_exists',
  [Code.InvalidArgument]: 'invalid_argument',
  [Code.Aborted]: 'token_replace_conflict',
  [Code.FailedPrecondition]: 'operator_state_conflict',
  [Code.Unavailable]: 'unavailable',
};

export function mapOperatorError(error: unknown): OperatorApiError {
  const directNetworkFailure = error instanceof TypeError;
  const connectError = ConnectError.from(error);
  const detail = connectError.findDetails(OperatorErrorDetailSchema)[0];
  const code = detail?.errorCode || ERROR_CODE_NAMES[connectError.code] || 'unknown';
  const networkFailure = directNetworkFailure || (connectError.code === Code.Unavailable && code !== 'audit_unavailable');
  return {
    code: networkFailure ? 'network_error' : code,
    message: connectError.rawMessage || 'Operator request failed.',
    fieldViolations: detail?.fieldViolations.map((violation) => ({
      field: violation.field,
      description: violation.description,
    })),
    retryable: networkFailure || code === 'audit_unavailable' || code === 'token_replace_conflict',
  };
}
