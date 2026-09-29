import { mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import ClusterListPage from './ClusterListPage.vue';
import { listClusters } from '@/connect/cluster-api';
import type * as ClusterApi from '@/connect/cluster-api';
import { useAuthStore } from '@/stores/auth';

vi.mock('@/connect/cluster-api', async (importOriginal) => {
  const original = await importOriginal<typeof ClusterApi>();
  return { ...original, listClusters: vi.fn() };
});

beforeEach(() => {
  setActivePinia(createPinia());
  vi.mocked(listClusters).mockReset();
});

describe('ClusterListPage', () => {
  it('shows an empty-state creation guide instead of a blank page', async () => {
    vi.mocked(listClusters).mockResolvedValue([]);
    useAuthStore().$patch({ user: { $typeName: 'auth.v1.SessionUser', id: 'admin-1', username: 'admin', roles: ['release_admin'], activeOrgId: 'org-1' } });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/customers/:customerId/clusters', name: 'ClusterList', component: ClusterListPage },
        { path: '/customers/:customerId/clusters/new', name: 'ClusterNew', component: { template: '<div />' } },
      ],
    });
    await router.push('/customers/customer-1/clusters');
    await router.isReady();

    const wrapper = mount(ClusterListPage, { global: { plugins: [router] } });
    await vi.waitFor(() => {
      expect(wrapper.text()).toContain('该客户下还没有集群。');
      expect(wrapper.text()).toContain('新建第一个集群');
    });
  });
});
