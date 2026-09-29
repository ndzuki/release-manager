import { Code, ConnectError } from '@connectrpc/connect';
import { create } from '@bufbuild/protobuf';
import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt';
import { PromotionMappingListSchema, PromotionMappingSchema } from '@/gen/orchestrator/v1/orchestrator_pb';
import {
  ListReleaseDefinitionsRequestSchema,
  UpdateReleaseDefinitionRequestSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import type { ReleaseDefinition } from '@/gen/common/v1/domain_pb';
import { orchestratorClient } from './client';
import { correlationLine, describeError } from './error-copy';

/*
 * Release definition management (REQ-040, A9 of the UX plan's missing surfaces).
 *
 * The console could only *show* a definition's name; it had no list and no editor,
 * although the promotion mapping is the prerequisite for converging an emergency
 * change into a standard ValuesRevision.
 *
 * Contract notes (api/proto/orchestrator/v1/orchestrator.proto):
 *  - ListReleaseDefinitions is organization-scoped and UNPAGINATED;
 *  - on the read message, `promotion_mappings` and `approved_annotation_keys` are
 *    JSON-encoded bytes: "decoding is the caller's job and an undecodable value is a
 *    contract violation, not a transport error" — so a bad payload is reported as a
 *    contract violation instead of an empty list;
 *  - UpdateReleaseDefinition is versioned ONLY when expected_version is supplied
 *    (stale -> FAILED_PRECONDITION + `optimistic_lock_conflict: …`), and unset fields
 *    are left alone, so we can send just the field we edit.
 */

export interface PromotionMappingView {
  workloadKind: string;
  workloadName: string;
  container: string;
  field: string;
  valuesPath: string;
}

export interface DefinitionView {
  id: string;
  name: string;
  customerId: string;
  clusterId: string;
  namespace: string;
  releaseName: string;
  chartName: string;
  status: string;
  version: bigint;
  hpaManaged: boolean;
  maxEmergencyReplicas: number;
  createdAt: string | null;
  updatedAt: string | null;
  promotionMappings: PromotionMappingView[];
  /** Set when the stored JSON could not be decoded — a contract violation. */
  promotionMappingsViolation: string | null;
}

export interface DefinitionFilters {
  customerId?: string;
  clusterId?: string;
  includeDisabled?: boolean;
}

export interface UpdateDefinitionInput {
  definitionId: string;
  /** Sent whenever the caller read a version, so a lost update is refused. */
  expectedVersion: bigint;
  namespace?: string;
  releaseName?: string;
  chartName?: string;
  promotionMappings?: PromotionMappingView[];
}

const REQUIRED_MAPPING_KEYS = ['workload_kind', 'workload_name', 'field', 'values_path'] as const;

/**
 * Decodes the JSON bytes the server stores for promotion mappings.
 *
 * The proto says an undecodable value is a CONTRACT VIOLATION, so "parseable but
 * wrong shape" counts too: `{}`, a bare string or an element that is not an object
 * are reported instead of silently becoming an empty mapping list.
 */
export function decodePromotionMappings(raw: Uint8Array): { value: PromotionMappingView[] | null; violation: string | null } {
  if (!raw || raw.length === 0) return { value: null, violation: null };
  const text = new TextDecoder().decode(raw);
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { value: null, violation: `无法解析服务端返回的 JSON（${text.slice(0, 80)}）` };
  }
  if (!Array.isArray(parsed)) {
    return { value: null, violation: `映射不是 JSON 数组（${text.slice(0, 80)}）` };
  }
  const mappings: PromotionMappingView[] = [];
  for (const [index, element] of parsed.entries()) {
    if (typeof element !== 'object' || element === null || Array.isArray(element)) {
      return { value: null, violation: `第 ${index + 1} 条映射不是对象（${text.slice(0, 80)}）` };
    }
    const record = element as Record<string, unknown>;
    const missing = REQUIRED_MAPPING_KEYS.filter((key) => typeof record[key] !== 'string');
    if (missing.length > 0) {
      return { value: null, violation: `第 ${index + 1} 条映射缺少字段 ${missing.join('/')}` };
    }
    mappings.push({
      workloadKind: record.workload_kind as string,
      workloadName: record.workload_name as string,
      container: typeof record.container === 'string' ? record.container : '',
      field: record.field as string,
      valuesPath: record.values_path as string,
    });
  }
  return { value: mappings, violation: null };
}

function toIso(value: Timestamp | undefined): string | null {
  return value ? timestampDate(value).toISOString() : null;
}

function toView(definition: ReleaseDefinition): DefinitionView {
  const decoded = decodePromotionMappings(definition.promotionMappings);
  const mappings = decoded.value ?? [];
  return {
    id: definition.id,
    name: definition.name,
    customerId: definition.customerId,
    clusterId: definition.clusterId,
    namespace: definition.namespace,
    releaseName: definition.releaseName,
    chartName: definition.chartName,
    status: definition.status,
    version: definition.version,
    hpaManaged: definition.hpaManaged,
    maxEmergencyReplicas: definition.maxEmergencyReplicas,
    createdAt: toIso(definition.createdAt),
    updatedAt: toIso(definition.updatedAt),
    promotionMappings: mappings,
    promotionMappingsViolation: decoded.violation,
  };
}

export async function listDefinitions(filters: DefinitionFilters = {}): Promise<DefinitionView[]> {
  const response = await orchestratorClient.listReleaseDefinitions(
    create(ListReleaseDefinitionsRequestSchema, {
      customerId: filters.customerId ?? '',
      clusterId: filters.clusterId ?? '',
      includeDisabled: filters.includeDisabled ?? true,
    }),
  );
  return response.definitions.map(toView);
}

export async function updateDefinition(input: UpdateDefinitionInput): Promise<DefinitionView> {
  const response = await orchestratorClient.updateReleaseDefinition(
    create(UpdateReleaseDefinitionRequestSchema, {
      definitionId: input.definitionId,
      expectedVersion: input.expectedVersion,
      namespace: input.namespace,
      releaseName: input.releaseName,
      chartName: input.chartName,
      /*
       * TASK-214: the mappings travel in the presence-carrying wrapper, so an EMPTY list
       * is a deliberate clear. The legacy `promotionMappings` field cannot express that:
       * proto3 repeated fields have no presence, so an empty list decodes to "absent" and
       * the server would leave the stored mappings alone while still answering 200.
       */
      promotionMappingsReplace: input.promotionMappings
        ? create(PromotionMappingListSchema, {
            items: input.promotionMappings.map((mapping) => create(PromotionMappingSchema, mapping)),
          })
        : undefined,
    }),
  );
  return toView(response.definition!);
}

export type DefinitionFailureCode =
  | 'conflict'
  | 'not_found'
  | 'invalid_input'
  | 'duplicate'
  | 'permission_denied'
  | 'unavailable';

export interface DefinitionFailure {
  code: DefinitionFailureCode;
  message: string;
  retryable: boolean;
  details: string;
}

const MESSAGES: Record<DefinitionFailureCode, string> = {
  conflict: '定义已被其他人修改（版本冲突），已刷新最新数据，请重试',
  not_found: '未找到该 Release Definition',
  invalid_input: '输入不合法：请检查映射字段与标识符',
  duplicate: '相同来源与目标的定义已存在',
  permission_denied: '无权修改定义：需要 release/write',
  unavailable: '操作失败，请稍后重试',
};

/**
 * UpdateReleaseDefinition sets no X-Reason-Code, but writes its refusals as
 * `optimistic_lock_conflict: …`, so the leading machine token is the carrier — never
 * a free-text substring anywhere in the message.
 */
export function mapDefinitionError(error: unknown): DefinitionFailure {
  const connectError = ConnectError.from(error);
  const raw = (connectError.rawMessage ?? connectError.message ?? '').trim();
  const token = raw.split(':', 1)[0]!.trim().toLowerCase();
  const pick = (code: DefinitionFailureCode, retryable = false): DefinitionFailure => ({
    code,
    message: MESSAGES[code],
    retryable,
    details: correlationLine(describeError(error)),
  });

  if (token === 'optimistic_lock_conflict') return pick('conflict', true);
  if (connectError.code === Code.NotFound) return pick('not_found');
  if (connectError.code === Code.AlreadyExists) return pick('duplicate');
  if (connectError.code === Code.PermissionDenied) return pick('permission_denied');
  if (connectError.code === Code.InvalidArgument) return pick('invalid_input');
  if (connectError.code === Code.FailedPrecondition) return pick('conflict', true);
  return pick('unavailable', true);
}
