import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useDefinitionsStore } from './definitions';
import * as api from '@/connect/definition-api';

vi.mock('@/connect/definition-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listDefinitions: vi.fn(), updateDefinition: vi.fn() };
});

const mockedList = vi.mocked(api.listDefinitions);
const mockedUpdate = vi.mocked(api.updateDefinition);

function definition(overrides: Partial<api.DefinitionView> = {}): api.DefinitionView {
  return {
    id: 'def-1',
    name: 'e2e-release-target',
    customerId: 'cust-1',
    clusterId: 'cluster-1',
    namespace: 'ns',
    releaseName: 'release-a',
    chartName: 'chart',
    status: 'active',
    version: 3n,
    hpaManaged: false,
    maxEmergencyReplicas: 0,
    createdAt: '2026-09-28T00:00:00Z',
    updatedAt: null,
    promotionMappings: [],
    promotionMappingsViolation: null,
    ...overrides,
  };
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue([definition()]);
  mockedUpdate.mockReset().mockResolvedValue(definition({ version: 4n }));
});

describe('definitions store', () => {
  it('lists definitions with the current filters', async () => {
    const store = useDefinitionsStore();

    await store.load({ customerId: 'cust-1', clusterId: '', includeDisabled: false });

    expect(mockedList).toHaveBeenCalledWith({ customerId: 'cust-1', clusterId: '', includeDisabled: false });
    expect(store.definitions.map((d) => d.name)).toEqual(['e2e-release-target']);
  });

  it('treats an empty result as empty, not as a failure', async () => {
    mockedList.mockResolvedValue([]);
    const store = useDefinitionsStore();

    await store.load();

    expect(store.isEmpty).toBe(true);
    expect(store.failure).toBeNull();
  });

  // A mapping payload we cannot decode is a contract violation and must be visible.
  it('surfaces definitions whose promotion JSON could not be decoded', async () => {
    mockedList.mockResolvedValue([definition({ promotionMappingsViolation: '无法解析服务端返回的 JSON' })]);
    const store = useDefinitionsStore();

    await store.load();

    expect(store.violations).toHaveLength(1);
  });

  it('sends the version it read and reloads after an update', async () => {
    const store = useDefinitionsStore();
    await store.load();
    mockedList.mockClear();

    const ok = await store.update({
      definitionId: 'def-1',
      expectedVersion: 3n,
      promotionMappings: [{ workloadKind: 'Deployment', workloadName: 'api', container: '', field: 'image', valuesPath: 'image.tag' }],
    });

    expect(ok).toBe(true);
    expect(mockedUpdate).toHaveBeenCalledWith(expect.objectContaining({ definitionId: 'def-1', expectedVersion: 3n }));
    expect(mockedList).toHaveBeenCalledTimes(1);
    expect(store.notice).toContain('版本 4');
  });

  // The server reports a stale version as `optimistic_lock_conflict` on
  // FAILED_PRECONDITION; the refresh must not wipe the message.
  it('reloads and reports an optimistic-lock conflict', async () => {
    const store = useDefinitionsStore();
    await store.load();
    mockedList.mockClear();
    mockedUpdate.mockRejectedValue(
      new ConnectError('optimistic_lock_conflict: expected version 3, current 5', Code.FailedPrecondition),
    );

    const ok = await store.update({ definitionId: 'def-1', expectedVersion: 3n });

    expect(ok).toBe(false);
    expect(store.failure?.code).toBe('conflict');
    expect(store.failure?.retryable).toBe(true);
    expect(mockedList).toHaveBeenCalledTimes(1);
  });

  it('reports an unknown definition as not found', async () => {
    const store = useDefinitionsStore();
    await store.load();
    mockedUpdate.mockRejectedValue(new ConnectError('definition "x" not found', Code.NotFound));

    await store.update({ definitionId: 'x', expectedVersion: 1n });

    expect(store.failure?.code).toBe('not_found');
  });
});
