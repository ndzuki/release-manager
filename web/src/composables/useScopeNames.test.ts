import { createPinia, setActivePinia } from 'pinia';
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { defineComponent } from 'vue';
import { createMemoryHistory, createRouter } from 'vue-router';
import { useScopeNames } from './useScopeNames';
import * as customerApi from '@/connect/customer-api';
import * as clusterApi from '@/connect/cluster-api';

vi.mock('@/connect/customer-api', async (importOriginal) => {
  const original = await importOriginal<typeof customerApi>();
  return { ...original, getCustomer: vi.fn() };
});
vi.mock('@/connect/cluster-api', async (importOriginal) => {
  const original = await importOriginal<typeof clusterApi>();
  return { ...original, getCluster: vi.fn() };
});

function probe(resolveCluster = true) {
  return defineComponent({
    setup() {
      const { customerName, clusterName } = useScopeNames({ resolveCluster });
      return { customerName, clusterName };
    },
    template: '<div data-testid="probe">{{ customerName }}|{{ clusterName }}</div>',
  });
}

async function mountAt(path: string) {
  const probeComponent = probe();
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/customers/:customerId/clusters/:clusterId/releases', component: probeComponent }],
  });
  await router.push(path);
  await router.isReady();
  const wrapper = mount(probeComponent, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
  vi.clearAllMocks();
});

describe('useScopeNames', () => {
  // The common path is in-app navigation, which already carries the names: no lookup.
  it('uses the query names without asking the server', async () => {
    const wrapper = await mountAt('/customers/c1/clusters/k1/releases?customerName=Acme&clusterName=Direct');

    expect(wrapper.get('[data-testid="probe"]').text()).toBe('Acme|Direct');
    expect(customerApi.getCustomer).not.toHaveBeenCalled();
    expect(clusterApi.getCluster).not.toHaveBeenCalled();
  });

  // Plan N7: a direct URL used to render raw identifiers as breadcrumb levels.
  it('resolves the names when the query does not carry them', async () => {
    vi.mocked(customerApi.getCustomer).mockResolvedValue({ id: 'c1', name: 'Acme' } as never);
    vi.mocked(clusterApi.getCluster).mockResolvedValue({ id: 'k1', name: 'Direct' } as never);
    const wrapper = await mountAt('/customers/c1/clusters/k1/releases');
    await flushPromises();

    expect(customerApi.getCustomer).toHaveBeenCalledWith('c1');
    expect(clusterApi.getCluster).toHaveBeenCalledWith('k1');
    expect(wrapper.get('[data-testid="probe"]').text()).toBe('Acme|Direct');
  });

  // A page that loads the cluster itself must not make the composable load it again
  // (ClusterDetailPage did, producing a duplicate GetCluster/GetClusterRoutes pair).
  it('can skip the cluster resolution when the page owns that load', async () => {
    vi.mocked(customerApi.getCustomer).mockResolvedValue({ id: 'c1', name: 'Acme' } as never);
    const Skipping = probe(false);
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/customers/:customerId/clusters/:clusterId/releases', component: Skipping }],
    });
    await router.push('/customers/c1/clusters/k1/releases');
    await router.isReady();
    await mount(Skipping, { global: { plugins: [router] } });
    await flushPromises();

    expect(clusterApi.getCluster).not.toHaveBeenCalled();
    expect(customerApi.getCustomer).toHaveBeenCalledWith('c1');
  });

  it('falls back to the identifier when the lookup fails', async () => {
    vi.mocked(customerApi.getCustomer).mockRejectedValue(new Error('boom'));
    vi.mocked(clusterApi.getCluster).mockRejectedValue(new Error('boom'));
    const wrapper = await mountAt('/customers/c1/clusters/k1/releases');
    await flushPromises();

    expect(wrapper.get('[data-testid="probe"]').text()).toBe('c1|k1');
  });
});
