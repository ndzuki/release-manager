import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useGovernanceStore } from './governance';
import * as api from '@/connect/binding-api';

vi.mock('@/connect/binding-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return {
    ...original,
    listBindings: vi.fn(),
    createBinding: vi.fn(),
    revokeBinding: vi.fn(),
    setCapabilityGrant: vi.fn(),
  };
});

const mockedList = vi.mocked(api.listBindings);
const mockedCreate = vi.mocked(api.createBinding);
const mockedRevoke = vi.mocked(api.revokeBinding);
const mockedGrant = vi.mocked(api.setCapabilityGrant);

const ORG = 'org-1';

function binding(customerId: string, status: 'active' | 'revoked' = 'active', version = 1n): api.BindingView {
  return {
    id: `binding-${customerId}`,
    orgId: ORG,
    customerId,
    status,
    optimisticVersion: version,
    createdAt: '2026-09-28T00:00:00Z',
    updatedAt: null,
  };
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue([binding('customer-a'), binding('customer-b', 'revoked', 3n)]);
  mockedCreate.mockReset().mockResolvedValue(undefined);
  mockedRevoke.mockReset().mockResolvedValue(undefined);
  mockedGrant.mockReset().mockResolvedValue({ sourceVersion: 7n, policyVersion: 9n });
});

describe('governance store', () => {
  it('keeps revoked bindings in the list (history is part of the contract)', async () => {
    const store = useGovernanceStore();

    await store.loadBindings(ORG);

    expect(mockedList).toHaveBeenCalledWith(ORG);
    expect(store.bindings.map((b) => b.status)).toEqual(['active', 'revoked']);
    // The page derives "active" rows for the duplicate guard without dropping history.
    expect(store.bindings.filter((b) => b.status === 'revoked')).toHaveLength(1);
  });

  it('binds a customer and reloads', async () => {
    const store = useGovernanceStore();
    await store.loadBindings(ORG);
    mockedList.mockClear();

    const ok = await store.bind('customer-c');

    expect(ok).toBe(true);
    expect(mockedCreate).toHaveBeenCalledWith(ORG, 'customer-c');
    expect(mockedList).toHaveBeenCalledTimes(1);
    expect(store.notice).toContain('customer-c');
  });

  // NOTE: binding_service.go writes no X-Reason-Code, so the mapping must work from
  // the code alone (the review flagged my first version of this test for faking a
  // reason header the server never sends).
  it('reports an active duplicate as a duplicate, not a generic failure', async () => {
    const store = useGovernanceStore();
    await store.loadBindings(ORG);
    mockedCreate.mockRejectedValue(new ConnectError('duplicate_binding', Code.AlreadyExists));

    const ok = await store.bind('customer-a');

    expect(ok).toBe(false);
    expect(store.failure?.code).toBe('duplicate');
    expect(store.failure?.retryable).toBe(false);
  });

  it('revokes with the version it loaded', async () => {
    const store = useGovernanceStore();
    await store.loadBindings(ORG);

    const ok = await store.revoke(binding('customer-a', 'active', 5n));

    expect(ok).toBe(true);
    expect(mockedRevoke).toHaveBeenCalledWith('binding-customer-a', 5n);
  });

  it('refreshes on a version conflict and says so', async () => {
    const store = useGovernanceStore();
    await store.loadBindings(ORG);
    mockedList.mockClear();
    mockedRevoke.mockRejectedValue(new ConnectError('optimistic_lock_conflict', Code.Aborted));

    const ok = await store.revoke(binding('customer-a'));

    expect(ok).toBe(false);
    expect(store.failure?.code).toBe('conflict');
    expect(store.failure?.retryable).toBe(true);
    expect(mockedList).toHaveBeenCalledTimes(1);
  });

  it('keeps the conflict message even when the refreshes itself fails', async () => {
    const store = useGovernanceStore();
    await store.loadBindings(ORG);
    mockedRevoke.mockRejectedValue(new ConnectError('optimistic_lock_conflict', Code.Aborted));
    mockedList.mockRejectedValue(new ConnectError('backend down', Code.Unavailable));

    const ok = await store.revoke(binding('customer-a'));

    expect(ok).toBe(false);
    // The refresh runs first; the conflict verdict must still be what the user sees.
    expect(store.failure?.code).toBe('conflict');
  });

  it('explains that an already revoked binding cannot be revoked twice', async () => {
    const store = useGovernanceStore();
    await store.loadBindings(ORG);
    mockedRevoke.mockRejectedValue(new ConnectError('binding_revoked', Code.FailedPrecondition));

    await store.revoke(binding('customer-b', 'revoked', 3n));

    expect(store.failure?.code).toBe('already_revoked');
    expect(store.failure?.message).toContain('已撤销');
  });

  // SetCapabilityGrant is convergent and versioned; there is no list RPC.
  it('reports the versions the capability grant answered', async () => {
    const store = useGovernanceStore();
    await store.loadBindings(ORG);

    const ok = await store.setCapability('user-1', 'release.emergency.resolve', false);

    expect(ok).toBe(true);
    expect(mockedGrant).toHaveBeenCalledWith(ORG, 'user-1', 'release.emergency.resolve', false);
    expect(store.grantVersions).toEqual({ sourceVersion: 7n, policyVersion: 9n });
    expect(store.notice).toContain('已授予');
  });

  it('maps a capability version conflict to a retryable conflict', async () => {
    const store = useGovernanceStore();
    await store.loadBindings(ORG);
    mockedGrant.mockRejectedValue(
      new ConnectError('version conflict', Code.Aborted, { 'X-Reason-Code': 'AUTHORIZATION_VERSION_CONFLICT' }),
    );

    const ok = await store.setCapability('user-1', 'release.values.approve', true);

    expect(ok).toBe(false);
    expect(store.failure?.code).toBe('conflict');
    expect(store.grantVersions).toBeNull();
  });

  it('maps a non-administrator refusal to a stable permission message', async () => {
    const store = useGovernanceStore();
    await store.loadBindings(ORG);
    mockedGrant.mockRejectedValue(new ConnectError('permission denied', Code.PermissionDenied));

    await store.setCapability('user-1', 'release.operation.create', false);

    expect(store.failure?.code).toBe('permission_denied');
    expect(store.failure?.message).toContain('组织管理员');
  });
});
