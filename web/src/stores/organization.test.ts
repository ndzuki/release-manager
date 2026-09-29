import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useOrganizationStore } from './organization';
import { useAuthStore } from './auth';
import * as api from '@/connect/organization-api';

vi.mock('@/connect/organization-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return {
    ...original,
    listMembers: vi.fn(),
    addMember: vi.fn(),
    updateMemberRole: vi.fn(),
    removeMember: vi.fn(),
  };
});

const mockedList = vi.mocked(api.listMembers);
const mockedUpdate = vi.mocked(api.updateMemberRole);
const mockedRemove = vi.mocked(api.removeMember);
const mockedAdd = vi.mocked(api.addMember);

const ORG = 'org-1';

function member(userId: string, role: api.OrganizationRole = 'viewer', version = 1n): api.OrganizationMemberView {
  return { userId, role, optimisticVersion: version, createdAt: '2026-09-28T00:00:00Z', updatedAt: null };
}

function signIn(roles: string[]): void {
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: {
      $typeName: 'auth.v1.SessionUser',
      id: 'me',
      username: 'me',
      roles,
      activeOrgId: ORG,
    },
  });
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue([member('u1', 'platform_admin'), member('u2')]);
  mockedUpdate.mockReset().mockResolvedValue(undefined);
  mockedRemove.mockReset().mockResolvedValue(undefined);
});

describe('organization members store', () => {
  it('loads the members of an organization', async () => {
    signIn(['platform_admin']);
    const store = useOrganizationStore();

    await store.load(ORG);

    expect(mockedList).toHaveBeenCalledWith(ORG);
    expect(store.members.map((m) => m.userId)).toEqual(['u1', 'u2']);
    expect(store.failure).toBeNull();
  });

  it('treats an empty organization as an empty list, not an error', async () => {
    signIn(['viewer']);
    mockedList.mockResolvedValue([]);
    const store = useOrganizationStore();

    await store.load(ORG);

    expect(store.members).toEqual([]);
    expect(store.failure).toBeNull();
  });

  // store.Role.CanGrant: release_admin cannot grant platform_admin.
  it('offers only grantable roles for a release_admin', async () => {
    signIn(['release_admin']);
    const store = useOrganizationStore();
    await store.load(ORG);

    expect(store.grantableRoles).toEqual(['release_admin', 'deployer', 'viewer']);
    expect(store.canWriteMembership).toBe(true);
  });

  it('offers nothing to a viewer', async () => {
    signIn(['viewer']);
    const store = useOrganizationStore();
    await store.load(ORG);

    expect(store.grantableRoles).toEqual([]);
    expect(store.canWriteMembership).toBe(false);
  });

  it('updates a role with the row version it loaded', async () => {
    signIn(['platform_admin']);
    const store = useOrganizationStore();
    await store.load(ORG);

    const ok = await store.changeRole(member('u2', 'viewer', 7n), 'deployer');

    expect(ok).toBe(true);
    expect(mockedUpdate).toHaveBeenCalledWith(ORG, 'u2', 'deployer', 7n);
    expect(store.notice).toContain('deployer');
  });

  // The contract reports a stale expected_version as ABORTED; the store must reload
  // rather than leave the table showing the pre-conflict state.
  it('reloads on an optimistic-lock conflict and says so', async () => {
    signIn(['platform_admin']);
    const store = useOrganizationStore();
    await store.load(ORG);
    mockedList.mockClear();
    mockedUpdate.mockRejectedValue(new ConnectError('optimistic lock conflict', Code.Aborted));

    const ok = await store.changeRole(member('u2', 'viewer', 1n), 'deployer');

    expect(ok).toBe(false);
    expect(store.failure?.code).toBe('conflict');
    expect(store.failure?.retryable).toBe(true);
    expect(mockedList).toHaveBeenCalledTimes(1);
  });

  it('explains the last-platform_admin precondition instead of a generic failure', async () => {
    signIn(['platform_admin']);
    const store = useOrganizationStore();
    await store.load(ORG);
    mockedRemove.mockRejectedValue(new ConnectError('no platform admin left', Code.FailedPrecondition));

    const ok = await store.remove(member('u1', 'platform_admin'));

    expect(ok).toBe(false);
    expect(store.failure?.code).toBe('last_admin');
    expect(store.failure?.message).toContain('platform_admin');
    expect(store.failure?.retryable).toBe(false);
  });

  // The server's real answer for a repeated AddMember is ALREADY_EXISTS +
  // X-Reason-Code: duplicate_member (the proto comment said INTERNAL).
  it('reports a duplicate add as "already a member", not a retryable failure', async () => {
    signIn(['platform_admin']);
    const store = useOrganizationStore();
    await store.load(ORG);
    mockedAdd.mockRejectedValue(new ConnectError('already a member', Code.AlreadyExists, { 'X-Reason-Code': 'duplicate_member' }));

    const ok = await store.add('u2', 'viewer');

    expect(ok).toBe(false);
    expect(store.failure?.code).toBe('duplicate');
    expect(store.failure?.retryable).toBe(false);
  });

  it('distinguishes a disabled organization from the last-admin rule', async () => {
    signIn(['platform_admin']);
    const store = useOrganizationStore();
    await store.load(ORG);
    mockedAdd.mockRejectedValue(
      new ConnectError('organization disabled', Code.FailedPrecondition, { 'X-Reason-Code': 'organization_disabled' }),
    );

    await store.add('u3', 'viewer');

    expect(store.failure?.code).toBe('org_disabled');
    expect(store.failure?.message).toContain('停用');
  });

  it('reports a missing member as not found', async () => {
    signIn(['platform_admin']);
    const store = useOrganizationStore();
    await store.load(ORG);
    mockedRemove.mockRejectedValue(new ConnectError('member not found', Code.NotFound));

    await store.remove(member('ghost'));

    expect(store.failure?.code).toBe('not_found');
  });

  it('removes a member and reloads the list', async () => {
    signIn(['platform_admin']);
    const store = useOrganizationStore();
    await store.load(ORG);
    mockedList.mockClear();

    const ok = await store.remove(member('u2', 'viewer', 4n));

    expect(ok).toBe(true);
    expect(mockedRemove).toHaveBeenCalledWith(ORG, 'u2', 4n);
    expect(mockedList).toHaveBeenCalledTimes(1);
    expect(store.notice).toContain('u2');
  });

  it('maps a permission failure to a stable message', async () => {
    signIn(['release_admin']);
    const store = useOrganizationStore();
    await store.load(ORG);
    mockedUpdate.mockRejectedValue(new ConnectError('permission denied', Code.PermissionDenied));

    await store.changeRole(member('u1', 'platform_admin'), 'viewer');

    expect(store.failure?.code).toBe('permission_denied');
    expect(store.failure?.message).toContain('organization/write');
  });
});
