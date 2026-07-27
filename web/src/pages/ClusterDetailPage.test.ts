import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import ClusterDetailPage from './ClusterDetailPage.vue';
import { getCluster } from '@/connect/cluster-api';
import type * as ClusterApi from '@/connect/cluster-api';
import { useAuthStore } from '@/stores/auth';

vi.mock('@/connect/cluster-api', async (importOriginal) => {
  const original = await importOriginal<typeof ClusterApi>();
  return { ...original, getCluster: vi.fn() };
});

beforeEach(() => {
  setActivePinia(createPinia());
  vi.unstubAllEnvs();
  vi.mocked(getCluster).mockReset().mockResolvedValue({
    id: 'cluster-1',
    name: 'staging',
    customerId: 'customer-1',
    enabled: true,
    version: 1,
    routeCount: 0,
    imageRules: [],
    chartRules: [],
  });
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: {
      $typeName: 'auth.v1.SessionUser',
      id: 'viewer-1',
      username: 'viewer',
      roles: ['viewer'],
      activeOrgId: 'org-1',
    },
  });
});

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters', name: 'ClusterList', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters/:clusterId', name: 'ClusterDetail', component: ClusterDetailPage },
      { path: '/customers/:customerId/clusters/:clusterId/operators', name: 'OperatorList', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters/:clusterId/edit', name: 'ClusterEdit', component: { template: '<div />' } },
    ],
  });
  await router.push('/customers/customer-1/clusters/cluster-1');
  await router.isReady();
  const wrapper = mount(ClusterDetailPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

describe('ClusterDetailPage operator navigation', () => {
  it('shows the read-only Operators entry to a viewer when the feature is enabled', async () => {
    const wrapper = await mountPage();

    const operatorsLink = wrapper.findAll('a').find((link) => link.text() === 'Operators');
    expect(operatorsLink?.attributes('href')).toBe('/customers/customer-1/clusters/cluster-1/operators');
    expect(wrapper.text()).not.toContain('Edit');
  });

  it('removes the Operators entry when the feature is disabled', async () => {
    vi.stubEnv('VITE_FEATURE_OPERATOR_MANAGEMENT', 'false');

    const wrapper = await mountPage();

    expect(wrapper.findAll('a').some((link) => link.text() === 'Operators')).toBe(false);
  });
});
