import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { authClient } from '@/connect/client';
import type * as Client from '@/connect/client';
import { organizationChangedEvent, useAuthStore } from './auth';

vi.mock('@/connect/client', async (importOriginal) => {
  const actual = await importOriginal<typeof Client>();
  return {
    ...actual,
    setAuthErrorHandler: vi.fn(),
    authClient: { ...actual.authClient, switchOrganization: vi.fn() },
  };
});

type SwitchResult = Awaited<ReturnType<typeof authClient.switchOrganization>>;

function session(activeOrgId: string): SwitchResult {
  return {
    user: {
      $typeName: 'auth.v1.SessionUser',
      id: 'user-1',
      username: 'admin',
      roles: ['platform_admin'],
      activeOrgId,
    },
    organizations: [{ $typeName: 'auth.v1.Organization', id: activeOrgId, name: activeOrgId }],
    expiresAt: 0n,
  } as unknown as SwitchResult;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

beforeEach(() => {
  setActivePinia(createPinia());
  vi.mocked(authClient.switchOrganization).mockReset();
});

describe('auth store organization switching (REQ-033 AC-033-09)', () => {
  it('keeps the last requested organization when responses arrive out of order', async () => {
    const store = useAuthStore();
    const first = deferred<SwitchResult>();
    const second = deferred<SwitchResult>();
    vi.mocked(authClient.switchOrganization)
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);

    const earlier = store.switchOrganization('org-a');
    const later = store.switchOrganization('org-b');

    // The later request answers first; the earlier one lands last.
    second.resolve(session('org-b'));
    await later;
    first.resolve(session('org-a'));
    await earlier;

    expect(store.user?.activeOrgId).toBe('org-b');
    expect(store.activeOrganization?.id).toBe('org-b');
  });

  it('does not announce a superseded switch', async () => {
    const store = useAuthStore();
    const first = deferred<SwitchResult>();
    const second = deferred<SwitchResult>();
    vi.mocked(authClient.switchOrganization)
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);

    const announced: string[] = [];
    const listener = (event: Event) => announced.push(String((event as CustomEvent).detail?.organizationId));
    globalThis.addEventListener(organizationChangedEvent, listener);

    const earlier = store.switchOrganization('org-a');
    const later = store.switchOrganization('org-b');
    second.resolve(session('org-b'));
    await later;
    first.resolve(session('org-a'));
    await earlier;

    globalThis.removeEventListener(organizationChangedEvent, listener);
    // The superseded switch must not clear the caches for the organization the
    // user has already left (D-72).
    expect(announced).toEqual(['org-b']);
  });

  it('ignores a switch that lands after the session was cleared', async () => {
    const store = useAuthStore();
    const pending = deferred<SwitchResult>();
    vi.mocked(authClient.switchOrganization).mockReturnValueOnce(pending.promise);

    const switching = store.switchOrganization('org-a');
    store.clearSession('expired');
    pending.resolve(session('org-a'));
    await switching;

    expect(store.user).toBeNull();
    expect(store.status).toBe('expired');
  });

  it('applies a lone switch normally', async () => {
    const store = useAuthStore();
    vi.mocked(authClient.switchOrganization).mockResolvedValueOnce(session('org-a'));

    await store.switchOrganization('org-a');

    expect(store.user?.activeOrgId).toBe('org-a');
    expect(store.status).toBe('authenticated');
  });
});
