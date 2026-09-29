import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useTrustPolicyStore } from './trustPolicy';
import { useAuthStore } from './auth';
import * as api from '@/connect/trust-api';

vi.mock('@/connect/trust-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return {
    ...original,
    getTrustPolicy: vi.fn(),
    endGrace: vi.fn(),
    retireTrustRoot: vi.fn(),
    revokeTrustRoot: vi.fn(),
  };
});

const mockedPolicy = vi.mocked(api.getTrustPolicy);
const mockedRetire = vi.mocked(api.retireTrustRoot);
const mockedRevoke = vi.mocked(api.revokeTrustRoot);

function root(keyId: string, state: api.TrustRootStateName, id = `root-${keyId}`): api.TrustRootView {
  return {
    id,
    keyId,
    issuer: 'CN=release-manager-dev',
    subjectPattern: 'release-manager',
    publicKeyPem: '-----BEGIN PUBLIC KEY-----',
    state,
    validFrom: '2026-09-28T00:00:00Z',
    graceUntil: state === 'grace' ? '2026-10-05T00:00:00Z' : null,
    createdAt: '2026-09-28T00:00:00Z',
    updatedAt: null,
    revokedAt: null,
  };
}

function policy(roots: api.TrustRootView[]) {
  return { environment: 'staging', version: 3n, revocationEpoch: 1n, roots };
}

beforeEach(() => {
  setActivePinia(createPinia());
  useAuthStore().$patch({ status: 'authenticated', initialized: true, user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-admin', roles: ['platform_admin'], activeOrgId: 'org-1' } });
  mockedPolicy.mockReset().mockResolvedValue(policy([root('key-new', 'active'), root('key-old', 'grace')]));
  mockedRetire.mockReset().mockResolvedValue(undefined);
  mockedRevoke.mockReset().mockResolvedValue(undefined);
});

describe('trust policy store', () => {
  it('loads the whole policy snapshot including retired and revoked roots', async () => {
    mockedPolicy.mockResolvedValue(policy([root('k1', 'active'), root('k2', 'retired'), root('k3', 'revoked')]));
    const store = useTrustPolicyStore();

    await store.load('staging');

    expect(mockedPolicy).toHaveBeenCalledWith('staging');
    expect(store.policy?.roots.map((r) => r.state)).toEqual(['active', 'retired', 'revoked']);
    expect(store.policy?.revocationEpoch).toBe(1n);
  });

  // An environment with no roots answers version 1 / epoch 0 and is NOT an error.
  it('treats an environment without roots as empty, not failed', async () => {
    mockedPolicy.mockResolvedValue({ environment: 'production', version: 1n, revocationEpoch: 0n, roots: [] });
    const store = useTrustPolicyStore();

    await store.load('production');

    expect(store.isEmpty).toBe(true);
    expect(store.failure).toBeNull();
    expect(store.liveRootCount).toBe(0);
  });

  it('does not count a pending root as live', async () => {
    mockedPolicy.mockResolvedValue(policy([root('k1', 'active'), root('k2', 'pending'), root('k3', 'retired')]));
    const store = useTrustPolicyStore();

    await store.load('staging');

    // One live root => the guard on the last live root must still bite.
    expect(store.liveRootCount).toBe(1);
    expect(store.canApply(root('k1', 'active'), 'retire')).toBe(false);
  });

  it('counts only live roots', async () => {
    mockedPolicy.mockResolvedValue(policy([root('k1', 'active'), root('k2', 'grace'), root('k3', 'retired')]));
    const store = useTrustPolicyStore();

    await store.load('staging');

    expect(store.liveRootCount).toBe(2);
  });

  // The server refuses to remove the last live root; the row must not offer it.
  it('refuses removal when only one live root remains', async () => {
    mockedPolicy.mockResolvedValue(policy([root('k1', 'active'), root('k2', 'retired')]));
    const store = useTrustPolicyStore();
    await store.load('staging');

    expect(store.canApply(root('k1', 'active'), 'retire')).toBe(false);
    expect(store.canApply(root('k1', 'active'), 'revoke')).toBe(false);
    expect(store.canApply(root('k2', 'retired'), 'retire')).toBe(false);
  });

  it('allows removal when another live root remains', async () => {
    const store = useTrustPolicyStore();
    await store.load('staging');

    expect(store.canApply(root('key-new', 'active'), 'retire')).toBe(true);
    expect(store.canApply(root('key-old', 'grace'), 'end_grace')).toBe(true);
  });

  it('retires with the operator identity taken from the session', async () => {
    const store = useTrustPolicyStore();
    await store.load('staging');

    const ok = await store.apply(root('key-new', 'active'), 'retire');

    expect(ok).toBe(true);
    expect(mockedRetire).toHaveBeenCalledWith('staging', 'root-key-new', 'dev-admin');
    expect(store.notice).toContain('key-new');
  });

  it('maps the last-live-root refusal to a stable message', async () => {
    const store = useTrustPolicyStore();
    await store.load('staging');
    mockedRevoke.mockRejectedValue(new ConnectError('last_root_removal_forbidden', Code.FailedPrecondition));

    const ok = await store.apply(root('key-new', 'active'), 'revoke');

    expect(ok).toBe(false);
    expect(store.failure?.code).toBe('last_live_root');
    expect(store.failure?.typed).toBe(true);
  });

  // Writes are state-ordered, so a stale action must re-read before reporting.
  it('reloads after a state refusal so the operator sees the real state', async () => {
    const store = useTrustPolicyStore();
    await store.load('staging');
    mockedPolicy.mockClear();
    mockedRetire.mockRejectedValue(new ConnectError('root is not active', Code.FailedPrecondition));

    await store.apply(root('key-new', 'active'), 'retire');

    expect(store.failure?.code).toBe('state_conflict');
    expect(mockedPolicy).toHaveBeenCalledTimes(1);
  });
});
