import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useLocalUsersStore } from './localUsers';
import { Code, ConnectError } from '@connectrpc/connect';
import * as api from '@/connect/local-user-api';

vi.mock('@/connect/local-user-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listLocalUsers: vi.fn(), createLocalUser: vi.fn() };
});

const mockedList = vi.mocked(api.listLocalUsers);

beforeEach(() => {
  setActivePinia(createPinia());
  vi.clearAllMocks();
});

describe('local users store', () => {
  it('replaces the page on load and keeps the cursor', async () => {
    mockedList.mockResolvedValue({
      users: [{ id: 'u1', username: 'alice', roles: [], orgId: 'o', status: 'active' }],
      nextCursor: 'c1',
    });
    const store = useLocalUsersStore();

    await store.load();

    expect(store.users).toHaveLength(1);
    expect(store.hasMore).toBe(true);
  });

  // A cursor the server cannot decode must not be retried: reload from page one and say
  // why, otherwise "load more" would fail forever on the same token.
  it('falls back to the first page when the cursor is stale', async () => {
    mockedList.mockResolvedValueOnce({
      users: [{ id: 'u1', username: 'alice', roles: [], orgId: 'o', status: 'active' }],
      nextCursor: 'stale',
    });
    const store = useLocalUsersStore();
    await store.load();

    // Then the stale-cursor failure, then the fallback reload (queues are consumed in order).
    const metadata = new Headers({ 'X-Reason-Code': 'invalid_cursor' });
    mockedList.mockRejectedValueOnce(new ConnectError('stale cursor', Code.InvalidArgument, metadata));
    mockedList.mockResolvedValueOnce({
      users: [{ id: 'u9', username: 'zed', roles: [], orgId: 'o', status: 'active' }],
      nextCursor: '',
    });
    await store.loadMore();

    expect(store.notice).not.toBe('');
    expect(store.users.map((u) => u.username)).toEqual(['zed']);
  });
});
