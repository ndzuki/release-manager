import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import { ConnectError, Code } from '@connectrpc/connect';
import type * as ClusterApi from '@/connect/cluster-api';
import type { Cluster } from '@/types/cluster';
import {
  createCluster,
  disableCluster,
  getCluster,
  listClusters,
  updateCluster,
} from '@/connect/cluster-api';
import { useClusterStore } from './cluster';

vi.mock('@/connect/cluster-api', async (importOriginal) => {
  const original = await importOriginal<typeof ClusterApi>();
  return {
    ...original,
    createCluster: vi.fn(),
    disableCluster: vi.fn(),
    getCluster: vi.fn(),
    listClusters: vi.fn(),
    updateCluster: vi.fn(),
  };
});

const updateClusterMock = vi.mocked(updateCluster);

function cluster(overrides: Partial<Cluster> = {}): Cluster {
  return {
    id: 'cluster-1',
    name: 'staging',
    customerId: 'customer-1',
    enabled: true,
    version: 3,
    routeCount: 1,
    imageRules: [{
      id: 'rule-1',
      clientKey: 'rule-1',
      artifactType: 'image',
      mode: 'direct',
      sourcePrefix: 'docker.io/library/',
      targetPrefix: 'harbor.example.com/proxy/',
    }],
    chartRules: [],
    ...overrides,
  };
}

beforeEach(() => {
  setActivePinia(createPinia());
  vi.clearAllMocks();
  vi.mocked(createCluster).mockResolvedValue(cluster({ version: 1, routeCount: 0, imageRules: [] }));
  vi.mocked(disableCluster).mockResolvedValue(undefined);
  vi.mocked(getCluster).mockResolvedValue(cluster());
  vi.mocked(listClusters).mockResolvedValue([]);
});

describe('cluster store save lifecycle', () => {
  it('treats a repeated identical save as success', async () => {
    updateClusterMock.mockResolvedValue(cluster());
    const store = useClusterStore();
    await store.loadCluster('cluster-1');

    const first = await store.save('customer-1', 'cluster-1');
    const second = await store.save('customer-1', 'cluster-1');

    expect(first?.id).toBe('cluster-1');
    expect(second?.id).toBe('cluster-1');
    expect(store.saveError).toBeNull();
    expect(updateClusterMock).toHaveBeenCalledTimes(2);
  });

  it('preserves the draft on optimistic lock conflict', async () => {
    updateClusterMock.mockRejectedValue(new Error('optimistic_lock_conflict: data was modified by another user'));
    const store = useClusterStore();
    await store.loadCluster('cluster-1');
    store.draft!.name = 'my unsaved edit';

    const saved = await store.save('customer-1', 'cluster-1');

    expect(saved).toBeNull();
    expect(store.draft?.name).toBe('my unsaved edit');
    expect(store.saveError?.code).toBe('optimistic_lock_conflict');
    expect(store.serverVersion).toBe(3);
  });

  it('preserves the draft and supports retry after network failure', async () => {
    updateClusterMock
      .mockRejectedValueOnce(new ConnectError('offline', Code.Unavailable))
      .mockResolvedValueOnce(cluster({ name: 'my unsaved edit', version: 4 }));
    const store = useClusterStore();
    await store.loadCluster('cluster-1');
    store.draft!.name = 'my unsaved edit';

    expect(await store.save('customer-1', 'cluster-1')).toBeNull();
    expect(store.draft?.name).toBe('my unsaved edit');
    expect(store.saveError?.code).toBe('network_error');

    const retried = await store.save('customer-1', 'cluster-1');
    expect(retried?.version).toBe(4);
    expect(store.saveError).toBeNull();
  });
  it('refreshes the server version without overwriting the draft', async () => {
    const store = useClusterStore();
    await store.loadCluster('cluster-1');
    store.draft!.name = 'my unsaved edit';
    vi.mocked(getCluster).mockResolvedValue(cluster({ name: 'server edit', version: 4 }));

    await store.refreshCluster('cluster-1');

    expect(store.current?.name).toBe('server edit');
    expect(store.serverVersion).toBe(4);
    expect(store.draft?.name).toBe('my unsaved edit');
    expect(store.draft?.version).toBe(4);
  });
});
