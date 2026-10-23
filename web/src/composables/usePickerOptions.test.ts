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
