import { Code, ConnectError } from '@connectrpc/connect';
import { correlationLine, describeError } from './error-copy';
import { create } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import {
  AddMemberRequestSchema,
  ListMembersRequestSchema,
  RemoveMemberRequestSchema,
  UpdateMemberRoleRequestSchema,
} from '@/gen/auth/v1/auth_pb';
import { organizationClient } from './client';

/**
 * Organization membership (REQ-026 / A1 of the UX plan's missing surfaces).
 *
 * Contract notes from api/proto/auth/v1/auth.proto that the UI must respect:
 *  - ListMembers is read-only and unpaginated; an empty organization is an empty
 *    list, not an error;
 *  - UpdateMemberRole uses optimistic locking and reports a stale
 *    `expected_version` as ABORTED;
 *  - both writes can fail with FAILED_PRECONDITION when they would leave the
 *    organization without any platform_admin (and AddMember also when the
 *    organization is disabled);
 *  - AddMember is NOT convergent: re-adding an existing member fails.
 */
export type OrganizationRole = 'platform_admin' | 'release_admin' | 'deployer' | 'viewer';

export const ORGANIZATION_ROLES: OrganizationRole[] = ['platform_admin', 'release_admin', 'deployer', 'viewer'];

export interface OrganizationMemberView {
  userId: string;
  role: OrganizationRole;
  optimisticVersion: bigint;
  createdAt: string | null;
  updatedAt: string | null;
}

/** Mirrors store.Role.CanGrant: release_admin cannot grant platform_admin. */
export function canGrant(grantor: OrganizationRole | undefined, target: OrganizationRole): boolean {
  if (grantor === 'platform_admin') return true;
  if (grantor === 'release_admin') return target !== 'platform_admin';
  return false;
}

/** The strongest role the caller holds in the organization, if any. */
export function highestRole(roles: string[] | undefined): OrganizationRole | undefined {
  const order: OrganizationRole[] = ['platform_admin', 'release_admin', 'deployer', 'viewer'];
  return order.find((role) => roles?.includes(role));
}

function toRole(value: string): OrganizationRole {
  return (ORGANIZATION_ROLES as string[]).includes(value) ? (value as OrganizationRole) : 'viewer';
}

export async function listMembers(orgId: string): Promise<OrganizationMemberView[]> {
  const response = await organizationClient.listMembers(create(ListMembersRequestSchema, { orgId }));
  return response.members.map((member) => ({
    userId: member.userId,
    role: toRole(member.role),
    optimisticVersion: member.optimisticVersion,
    createdAt: member.createdAt ? timestampDate(member.createdAt).toISOString() : null,
    updatedAt: member.updatedAt ? timestampDate(member.updatedAt).toISOString() : null,
  }));
}

export async function addMember(orgId: string, userId: string, role: OrganizationRole): Promise<void> {
  await organizationClient.addMember(create(AddMemberRequestSchema, { orgId, userId, role }));
}

export async function updateMemberRole(
  orgId: string,
  userId: string,
  newRole: OrganizationRole,
  expectedVersion: bigint,
): Promise<void> {
  await organizationClient.updateMemberRole(
    create(UpdateMemberRoleRequestSchema, { orgId, userId, newRole, expectedVersion }),
  );
}

export async function removeMember(orgId: string, userId: string, expectedVersion = 0n): Promise<void> {
  await organizationClient.removeMember(create(RemoveMemberRequestSchema, { orgId, userId, expectedVersion }));
}

export type MemberFailureCode =
  | 'duplicate'
  | 'permission_denied'
  | 'conflict'
  | 'last_admin'
  | 'org_disabled'
  | 'not_found'
  | 'invalid_role'
  | 'unavailable';

export interface MemberFailure {
  /** Stable correlation data for the technical-details line (never payload data). */
  details: string;
  code: MemberFailureCode;
  message: string;
  retryable: boolean;
}

const MESSAGES: Record<MemberFailureCode, string> = {
  // The server answers ALREADY_EXISTS + X-Reason-Code: duplicate_member for a
  // repeated AddMember (the proto comment claiming INTERNAL was stale and is
  // corrected alongside this change).
  duplicate: '该用户已是组织成员；请直接修改其角色',
  permission_denied: '无权修改该组织的成员（需要 organization/write，或被授予的角色高于你的权限）',
  conflict: '成员信息已被其他人修改，已为你刷新最新数据，请重试',
  last_admin: '不能移除或降级最后一个 platform_admin，组织必须保留管理员',
  org_disabled: '该组织已停用，无法修改成员',
  not_found: '成员或组织不存在',
  invalid_role: '未知角色',
  unavailable: '操作失败，请稍后重试',
};

export function mapMemberError(error: unknown): MemberFailure {
  // The shared layer owns the copy for stable reason codes; the local function
  // keeps this feature's code classification (which drives reload/retry).
  return withSharedCopy(mapMemberErrorLocal(error), error);
}

function mapMemberErrorLocal(error: unknown): MemberFailure {
  const connectError = ConnectError.from(error);
  const reason = (connectError.metadata.get('X-Reason-Code') ?? '').toLowerCase();
  const pick = (code: MemberFailureCode, retryable = false): MemberFailure => ({ code, message: MESSAGES[code], retryable, details: '' });

  if (connectError.code === Code.AlreadyExists || reason.includes('duplicate')) return pick('duplicate');
  if (connectError.code === Code.Aborted || reason.includes('conflict')) return pick('conflict', true);
  if (connectError.code === Code.PermissionDenied) return pick('permission_denied');
  if (connectError.code === Code.FailedPrecondition) {
    return reason.includes('disabled') ? pick('org_disabled') : pick('last_admin');
  }
  if (connectError.code === Code.NotFound) return pick('not_found');
  if (connectError.code === Code.InvalidArgument) return pick('invalid_role');
  return pick('unavailable', true);
}

/** Centralised copy for a known reason code overrides the local fallback wording. */
function withSharedCopy(failure: MemberFailure, error: unknown): MemberFailure {
  const described = describeError(error);
  const shared = described.message;
  return {
    ...failure,
    message: shared ?? failure.message,
    details: correlationLine(described),
  };
}
