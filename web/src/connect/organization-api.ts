import { Code, ConnectError } from '@connectrpc/connect';
import { correlationLine, describeError } from './error-copy';
import { create } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import {
  AddMemberRequestSchema,
  CreateOrganizationRequestSchema,
  DisableOrganizationRequestSchema,
  GetOrganizationRequestSchema,
  ListMembersRequestSchema,
  RemoveMemberRequestSchema,
  UpdateMemberRoleRequestSchema,
  UpdateOrganizationRequestSchema,
} from '@/gen/auth/v1/auth_pb';
import type { Organization } from '@/gen/auth/v1/auth_pb';
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
function withSharedCopy<T extends { message: string; details: string }>(failure: T, error: unknown): T {
  const described = describeError(error);
  const shared = described.message;
  return {
    ...failure,
    message: shared ?? failure.message,
    details: correlationLine(described),
  };
}

/*
 * Organization-level writes (REQ-026).
 *
 * The console could only manage MEMBERSHIP: the four member RPCs have wrappers,
 * while creating, renaming and disabling an organization had none, so the console
 * could not create an organization at all. Contract notes from
 * api/proto/auth/v1/auth.proto that the UI must respect:
 *  - CreateOrganization is platform_admin-only (the handler re-checks the role on
 *    top of the policy) and adds the caller to the new organization as
 *    platform_admin, but that membership write is BEST EFFORT: the organization
 *    still exists when it fails and the call reports success, so the caller must
 *    confirm membership afterwards and must not claim the user is an admin;
 *  - it is NOT idempotent (no deduplication key, names are not unique), so a retry
 *    after a lost response creates a SECOND organization;
 *  - UpdateOrganization and DisableOrganization are versioned writes: the caller
 *    must send the version it read, and a stale one is refused with ABORTED
 *    (optimistic lock conflict). Organization names are not the console's only
 *    source of a version — the session snapshot goes stale as soon as anyone
 *    writes — which is why `getOrganization` is exported alongside them;
 *  - a disabled organization cannot be updated (FAILED_PRECONDITION +
 *    X-Reason-Code: organization_disabled), and disabling is TERMINAL: no
 *    procedure reverses it.
 */

/**
 * The statuses this console can read off the wire. `unknown` is the console's own
 * reading of any other value: `Organization.status` is a free-form string
 * (api/proto/auth/v1/auth.proto) and the server passes the stored column through
 * verbatim (internal/auth/org_service.go toProtoOrg -> `string(o.Status)`), so a
 * value this console does not know is a value it must not classify.
 */
export type OrganizationStatus = 'active' | 'disabled' | 'unknown';

export interface OrganizationView {
  id: string;
  name: string;
  /** 'active' | 'disabled'; disabling is terminal. 'unknown' means unreadable. */
  status: OrganizationStatus;
  optimisticVersion: bigint;
  createdAt: string | null;
  updatedAt: string | null;
}

/** Only a wire value this console can read becomes a state; anything else stays unknown. */
function organizationStatus(status: string): OrganizationStatus {
  return status === 'active' || status === 'disabled' ? status : 'unknown';
}

function toOrganizationView(organization: Organization): OrganizationView {
  return {
    id: organization.id,
    name: organization.name,
    status: organizationStatus(organization.status),
    optimisticVersion: organization.optimisticVersion,
    createdAt: organization.createdAt ? timestampDate(organization.createdAt).toISOString() : null,
    updatedAt: organization.updatedAt ? timestampDate(organization.updatedAt).toISOString() : null,
  };
}

/**
 * Reads one organization. The console needs this to obtain the CURRENT
 * `optimisticVersion`: the version in the session snapshot is from login time, and
 * both writes compare-and-swap on it, so a rename or disable that starts from the
 * snapshot fails with ABORTED as soon as anyone else has written the row.
 */
export async function getOrganization(orgId: string): Promise<OrganizationView> {
  const response = await organizationClient.getOrganization(create(GetOrganizationRequestSchema, { orgId }));
  return toOrganizationView(response.organization!);
}

/** Creates an organization and (best effort) the caller's platform_admin membership. */
export async function createOrganization(name: string): Promise<OrganizationView> {
  const response = await organizationClient.createOrganization(
    create(CreateOrganizationRequestSchema, { name: name.trim() }),
  );
  return toOrganizationView(response.organization!);
}

export async function updateOrganization(
  orgId: string,
  name: string,
  expectedVersion: bigint,
): Promise<OrganizationView> {
  const response = await organizationClient.updateOrganization(
    create(UpdateOrganizationRequestSchema, { orgId, name: name.trim(), expectedVersion }),
  );
  return toOrganizationView(response.organization!);
}

export async function disableOrganization(orgId: string, expectedVersion: bigint): Promise<OrganizationView> {
  const response = await organizationClient.disableOrganization(
    create(DisableOrganizationRequestSchema, { orgId, expectedVersion }),
  );
  return toOrganizationView(response.organization!);
}

export type OrganizationFailureCode =
  | 'permission_denied'
  | 'not_found'
  | 'conflict'
  | 'org_disabled'
  | 'invalid_input'
  | 'unavailable';

export interface OrganizationFailure {
  /** Stable correlation data for the technical-details line (never payload data). */
  details: string;
  code: OrganizationFailureCode;
  message: string;
  retryable: boolean;
}

const ORGANIZATION_MESSAGES: Record<OrganizationFailureCode, string> = {
  permission_denied: '无权管理组织：创建组织需要 platform_admin，改名与停用需要 organization/write',
  not_found: '组织不存在（可能已被删除）',
  conflict: '组织信息已被其他人修改，已为你刷新最新数据，请重试',
  // Same wording as the catalog's `reason.organization_disabled`: the reason code
  // serves organization-level writes too, so the shared copy was widened from
  // "cannot modify members" to the procedure-neutral sentence.
  org_disabled: '该组织已停用，无法再执行该操作',
  invalid_input: '输入不合法：请检查组织名称',
  unavailable: '操作失败，请稍后重试',
};

/**
 * Classifies an organization write by the carriers the repo trusts: the stable
 * X-Reason-Code first, then the Connect status. The classification matches
 * {@link mapMemberError} for the codes the two surfaces share (`conflict`,
 * `permission_denied`, `not_found`, `org_disabled`, `unavailable`), so a reason
 * code cannot mean two different things on the same feature.
 */
export function mapOrganizationError(error: unknown): OrganizationFailure {
  return withSharedCopy(mapOrganizationErrorLocal(error), error);
}

function mapOrganizationErrorLocal(error: unknown): OrganizationFailure {
  const connectError = ConnectError.from(error);
  const reason = (connectError.metadata.get('X-Reason-Code') ?? '').toLowerCase();
  const pick = (code: OrganizationFailureCode, retryable = false): OrganizationFailure => ({
    code,
    message: ORGANIZATION_MESSAGES[code],
    retryable,
    details: '',
  });

  // Aborted is the only carrier DisableOrganization sets for a lost race (its
  // handler has no reason code there), so both carriers must be honoured.
  if (reason.includes('optimistic_lock_conflict') || connectError.code === Code.Aborted) return pick('conflict', true);
  if (reason.includes('organization_disabled')) return pick('org_disabled');
  if (connectError.code === Code.PermissionDenied) return pick('permission_denied');
  if (connectError.code === Code.NotFound) return pick('not_found');
  // Reached on UpdateOrganization only: the handler refuses a disabled
  // organization with FAILED_PRECONDITION + organization_disabled (matched above).
  if (connectError.code === Code.FailedPrecondition) return pick('org_disabled');
  if (connectError.code === Code.InvalidArgument) return pick('invalid_input');
  return pick('unavailable', true);
}
