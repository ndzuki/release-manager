import { Code, ConnectError } from '@connectrpc/connect';
import { correlationLine, describeError } from './error-copy';
import { create } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import {
  CreateBindingRequestSchema,
  ListBindingsRequestSchema,
  RevokeBindingRequestSchema,
  SetCapabilityGrantRequestSchema,
} from '@/gen/auth/v1/auth_pb';
import { bindingClient, authorizationClient } from './client';

/*
 * Organization -> customer bindings and explicit capability grants (REQ-049 /
 * REQ-027, A2 + A3 of the UX plan's missing surfaces).
 *
 * Contract notes (api/proto/auth/v1/auth.proto) the UI must respect:
 *  - CreateBinding is idempotent on the (organization, customer) pair: an active
 *    pair answers ALREADY_EXISTS (duplicate_binding), and reactivating a revoked
 *    one is a versioned update that answers ABORTED on a lost race;
 *  - RevokeBinding is versioned: a stale expected_version answers ABORTED and an
 *    already revoked binding answers FAILED_PRECONDITION (binding_revoked);
 *  - ListBindings includes revoked rows (history is kept), so the table must show
 *    status rather than filtering them out silently;
 *  - SetCapabilityGrant is convergent and versioned (ABORTED on
 *    AUTHORIZATION_VERSION_CONFLICT); there is NO list-grants RPC, so the console
 *    can only apply a grant/revoke for a (subject, action) pair and report the
 *    versions the server answered.
 */

/** store.AuthorizationAction values (REQ-027). */
export const AUTHORIZATION_ACTIONS = [
  'release.emergency.execute',
  'release.emergency.resolve',
  'release.values.create',
  'release.values.approve',
  'release.operation.create',
] as const;

export type AuthorizationAction = (typeof AUTHORIZATION_ACTIONS)[number];

export interface BindingView {
  id: string;
  orgId: string;
  customerId: string;
  status: 'active' | 'revoked';
  optimisticVersion: bigint;
  createdAt: string | null;
  updatedAt: string | null;
}

export interface GrantVersions {
  sourceVersion: bigint;
  policyVersion: bigint;
}

export async function listBindings(orgId: string): Promise<BindingView[]> {
  const response = await bindingClient.listBindings(create(ListBindingsRequestSchema, { orgId }));
  return response.bindings.map((binding: (typeof response.bindings)[number]) => ({
    id: binding.id,
    orgId: binding.orgId,
    customerId: binding.customerId,
    status: binding.status === 'active' ? 'active' : 'revoked',
    optimisticVersion: binding.optimisticVersion,
    createdAt: binding.createdAt ? timestampDate(binding.createdAt).toISOString() : null,
    updatedAt: binding.updatedAt ? timestampDate(binding.updatedAt).toISOString() : null,
  }));
}

export async function createBinding(orgId: string, customerId: string): Promise<void> {
  await bindingClient.createBinding(create(CreateBindingRequestSchema, { orgId, customerId }));
}

export async function revokeBinding(bindingId: string, expectedVersion: bigint): Promise<void> {
  await bindingClient.revokeBinding(create(RevokeBindingRequestSchema, { bindingId, expectedVersion }));
}

export async function setCapabilityGrant(
  organizationId: string,
  subject: string,
  action: AuthorizationAction,
  revoked: boolean,
): Promise<GrantVersions> {
  const response = await authorizationClient.setCapabilityGrant(
    create(SetCapabilityGrantRequestSchema, { organizationId, subject, action, revoked }),
  );
  return { sourceVersion: response.sourceVersion, policyVersion: response.policyVersion };
}

export type GovernanceFailureCode =
  | 'duplicate'
  | 'conflict'
  | 'already_revoked'
  | 'permission_denied'
  | 'not_found'
  | 'invalid_input'
  | 'unavailable';

export interface GovernanceFailure {
  /** Stable correlation data for the technical-details line (never payload data). */
  details: string;
  code: GovernanceFailureCode;
  message: string;
  retryable: boolean;
}

const MESSAGES: Record<GovernanceFailureCode, string> = {
  duplicate: '该组织与客户的绑定已存在（正在生效），无需重复创建',
  conflict: '记录已被其他人修改，已为你刷新最新数据，请重试',
  already_revoked: '该绑定已撤销',
  permission_denied: '无权执行该操作：需要组织管理员，且主体必须是该组织的有效成员',
  not_found: '未找到对应的组织、客户或绑定',
  invalid_input: '输入不合法：请检查标识符与能力名称',
  unavailable: '操作失败，请稍后重试',
};

/** Shared mapper for both services: the reason code disambiguates same-code cases. */
export function mapGovernanceError(error: unknown): GovernanceFailure {
  // The shared layer owns the copy for stable reason codes; the local function
  // keeps this feature's code classification (which drives reload/retry).
  return withSharedCopy(mapGovernanceErrorLocal(error), error);
}

function mapGovernanceErrorLocal(error: unknown): GovernanceFailure {
  const connectError = ConnectError.from(error);
  const reason = (connectError.metadata.get('X-Reason-Code') ?? '').toLowerCase();
  const pick = (code: GovernanceFailureCode, retryable = false): GovernanceFailure => ({
    code,
    message: MESSAGES[code],
    retryable,
    details: '',
  });

  if (reason.includes('duplicate')) return pick('duplicate');
  if (reason.includes('revoked')) return pick('already_revoked');
  if (reason.includes('conflict')) return pick('conflict', true);
  if (connectError.code === Code.AlreadyExists) return pick('duplicate');
  if (connectError.code === Code.Aborted) return pick('conflict', true);
  if (connectError.code === Code.PermissionDenied) return pick('permission_denied');
  if (connectError.code === Code.NotFound) return pick('not_found');
  if (connectError.code === Code.InvalidArgument) return pick('invalid_input');
  if (connectError.code === Code.FailedPrecondition) return pick('already_revoked');
  return pick('unavailable', true);
}

/** Centralised copy for a known reason code overrides the local fallback wording. */
function withSharedCopy(failure: GovernanceFailure, error: unknown): GovernanceFailure {
  const described = describeError(error);
  const shared = described.message;
  return {
    ...failure,
    message: shared ?? failure.message,
    details: correlationLine(described),
  };
}
