import { describe, expect, it, vi } from 'vitest';
import { usePickerOptions, type PickerLoaders } from './usePickerOptions';

/*
 * Picker lists for the "pick, don't type" surfaces. The loaders are injected so these
 * cases exercise the caching/last-write rules without a Connect client: the rules are
 * what breaks when a slow response for customer A lands after the user picked B.
 */

function loaders(overrides: Partial<PickerLoaders> = {}) {
  return {
    customers: vi.fn().mockResolvedValue([
      { id: 'cust-1', name: 'Cust One', slug: 'cust-one', status: 'active', version: 1 },
    ]),
    clusters: vi.fn().mockResolvedValue([
      { id: 'prod-west', name: 'Prod West', customerId: 'cust-1', enabled: true, version: 1, routeCount: 0 },
    ]),
    definitions: vi.fn().mockResolvedValue([]),
    ...overrides,
  };
}

describe('usePickerOptions', () => {
  it('loads customers once and keeps the list', async () => {
    const spies = loaders();
    const picker = usePickerOptions(spies);

    await picker.loadCustomers(true);
    await picker.loadCustomers(true);

    expect(spies.customers).toHaveBeenCalledTimes(1);
    expect(picker.customers.value).toHaveLength(1);
    expect(picker.loadingCustomers.value).toBe(false);
  });

  it('caches clusters per customer and clears on demand', async () => {
    const spies = loaders();
    const picker = usePickerOptions(spies);

    await picker.loadClusters('cust-1');
    await picker.loadClusters('cust-1');
    expect(spies.clusters).toHaveBeenCalledTimes(1);
    expect(picker.clusters.value.map((cluster) => cluster.id)).toEqual(['prod-west']);

    picker.clearClusters();
    expect(picker.clusters.value).toEqual([]);
    // A cleared list is not a cache miss: the customer's clusters are still remembered.
    await picker.loadClusters('cust-1');
    expect(spies.clusters).toHaveBeenCalledTimes(1);
    expect(picker.clusters.value).toHaveLength(1);
  });

  it('drops a stale cluster response so the current customer keeps its own list', async () => {
    // The user picks customer A (slow), then customer B (fast). B must win even though
    // A's response arrives last — otherwise the picker shows A's clusters under B.
    const deferred = new Map<string, (value: Array<{ id: string; name: string; customerId: string; enabled: boolean; version: number; routeCount: number }>) => void>();
    const clusters = vi.fn((customerId: string) => new Promise<Array<{ id: string; name: string; customerId: string; enabled: boolean; version: number; routeCount: number }>>((resolve) => {
      deferred.set(customerId, resolve);
    }));
    const picker = usePickerOptions({ clusters });

    const slow = picker.loadClusters('cust-a');
    const fast = picker.loadClusters('cust-b');
    deferred.get('cust-b')!([{ id: 'b-1', name: 'B', customerId: 'cust-b', enabled: true, version: 1, routeCount: 0 }]);
    await fast;
    deferred.get('cust-a')!([{ id: 'a-1', name: 'A', customerId: 'cust-a', enabled: true, version: 1, routeCount: 0 }]);
    await slow;

    expect(picker.clusters.value.map((cluster) => cluster.id)).toEqual(['b-1']);
    expect(picker.loadingClusters.value).toBe(false);
    expect(picker.clustersError.value).toBeNull();
  });

  it('clearing the list invalidates an in-flight response', async () => {
    let resolveSlow: ((value: Array<{ id: string; name: string; customerId: string; enabled: boolean; version: number; routeCount: number }>) => void) | undefined;
    const clusters = vi.fn(() => new Promise<Array<{ id: string; name: string; customerId: string; enabled: boolean; version: number; routeCount: number }>>((resolve) => {
      resolveSlow = resolve;
    }));
    const picker = usePickerOptions({ clusters });

    const pending = picker.loadClusters('cust-a');
    picker.clearClusters();
    resolveSlow!([{ id: 'a-1', name: 'A', customerId: 'cust-a', enabled: true, version: 1, routeCount: 0 }]);
    await pending;

    expect(picker.clusters.value).toEqual([]);
    expect(picker.loadingClusters.value).toBe(false);
  });

  it('empty customer clears the cluster list without a request', async () => {
    const spies = loaders();
    const picker = usePickerOptions(spies);

    await picker.loadClusters('');
    expect(spies.clusters).not.toHaveBeenCalled();
    expect(picker.clusters.value).toEqual([]);
  });

  it('records a failed list load instead of throwing', async () => {
    const spies = loaders({ customers: vi.fn().mockRejectedValue(new Error('boom')) });
    const picker = usePickerOptions(spies);

    await expect(picker.loadCustomers(true)).resolves.toBeUndefined();
    expect(picker.customers.value).toEqual([]);
    expect(picker.customersError.value).toBe('客户列表读取失败');
    expect(picker.loadingCustomers.value).toBe(false);
  });

  it('loads definitions once for the bundle filter', async () => {
    const spies = loaders();
    const picker = usePickerOptions(spies);

    await picker.loadDefinitions();
    await picker.loadDefinitions();

    expect(spies.definitions).toHaveBeenCalledTimes(1);
    expect(picker.loadingDefinitions.value).toBe(false);
  });
});
