import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import ClusterListPage from './ClusterListPage.vue';
import { listClusters } from '@/connect/cluster-api';
import type * as ClusterApi from '@/connect/cluster-api';
import { getCustomer } from '@/connect/customer-api';
import type * as CustomerApi from '@/connect/customer-api';
import { useAuthStore } from '@/stores/auth';

vi.mock('@/connect/cluster-api', async (importOriginal) => {
  const original = await importOriginal<typeof ClusterApi>();
  return { ...original, listClusters: vi.fn() };
});

/* Direction C (TASK-394): the breadcrumb resolves the CUSTOMER NAME through
 * `useScopeNames`, which falls back to the API when the query does not carry it. */
vi.mock('@/connect/customer-api', async (importOriginal) => {
  const original = await importOriginal<typeof CustomerApi>();
  return { ...original, getCustomer: vi.fn() };
});

beforeEach(() => {
  setActivePinia(createPinia());
  vi.mocked(listClusters).mockReset();
  vi.mocked(getCustomer).mockReset();
  vi.mocked(getCustomer).mockRejectedValue(new Error('not found'));
});

async function mountList(path: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters', name: 'ClusterList', component: ClusterListPage },
      { path: '/customers/:customerId/clusters/new', name: 'ClusterNew', component: { template: '<div />' } },
    ],
  });
  await router.push(path);
  await router.isReady();
  const wrapper = mount(ClusterListPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

describe('ClusterListPage', () => {
  it('shows an empty-state creation guide instead of a blank page', async () => {
    vi.mocked(listClusters).mockResolvedValue([]);
    useAuthStore().$patch({ user: { $typeName: 'auth.v1.SessionUser', id: 'admin-1', username: 'admin', roles: ['release_admin'], activeOrgId: 'org-1' } });

    const wrapper = await mountList('/customers/customer-1/clusters');

    expect(wrapper.text()).toContain('该客户下还没有集群。');
    expect(wrapper.text()).toContain('新建第一个集群');
  });

  /*
   * TASK-394: the page header is the shared PageHeader now. It has no eyebrow here (the
   * audit found no category label on this page) and the crumb names the customer, never
   * the `:customerId` route parameter.
   */
  describe('direction C page header', () => {
    it('renders no eyebrow and takes the title from the catalog', async () => {
      vi.mocked(listClusters).mockResolvedValue([]);

      const wrapper = await mountList('/customers/customer-1/clusters');

      expect(wrapper.find('.page-header__eyebrow').exists()).toBe(false);
      expect(wrapper.get('.page-header__title').text()).toBe('集群');
    });

    it('names the customer in the breadcrumb when the query carries the name', async () => {
      vi.mocked(listClusters).mockResolvedValue([]);

      const wrapper = await mountList('/customers/customer-1/clusters?customerName=Acme');

      const crumbs = wrapper.get('.page-header__crumbs').text();
      expect(crumbs).toContain('Acme');
      expect(crumbs).not.toContain('customer-1');
    });

    it('resolves the name from the API on a direct URL', async () => {
      vi.mocked(listClusters).mockResolvedValue([]);
      vi.mocked(getCustomer).mockResolvedValue({ id: 'customer-1', name: 'Acme', slug: 'acme', status: 'active', version: 1 } as never);

      const wrapper = await mountList('/customers/customer-1/clusters');
      await flushPromises();

      expect(wrapper.get('.page-header__crumbs').text()).toContain('Acme');
      expect(wrapper.get('.page-header__crumbs').text()).not.toContain('customer-1');
    });

    it('omits the level instead of printing the identifier when the name is unknown', async () => {
      vi.mocked(listClusters).mockResolvedValue([]);

      const wrapper = await mountList('/customers/customer-1/clusters');
      await flushPromises();

      // No resolved name: no trail at all rather than a UUID crumb.
      expect(wrapper.find('.page-header__crumbs').exists()).toBe(false);
    });
  });
});
