import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import { defineComponent } from 'vue';
import CustomerListPage from './CustomerListPage.vue';
import CustomerDetailPage from './CustomerDetailPage.vue';
import { useAuthStore } from '@/stores/auth';

/*
 * Plan N2: the customer subtree had no path to clusters — the detail page's only link
 * was "Back", so the release subtree could only be reached by typing a URL. These
 * cases pin the entries that close that hole.
 */
vi.mock('@/connect/customer-api', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/connect/customer-api')>();
  return { ...original, listCustomers: vi.fn(), getCustomer: vi.fn(), listCustomerEvents: vi.fn() };
});

const Stub = defineComponent({ template: '<div />' });

async function mountCustomers(path: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers', name: 'CustomerList', component: CustomerListPage },
      { path: '/customers/:customerId/clusters', name: 'ClusterList', component: Stub },
      { path: '/customers/:id', name: 'CustomerDetail', component: Stub },
      { path: '/customers/new', name: 'CustomerNew', component: Stub },
      { path: '/', name: 'Home', component: Stub },
    ],
  });
  await router.push(path);
  await router.isReady();
  const wrapper = mount(CustomerListPage, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

beforeEach(() => {
  setActivePinia(createPinia());
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-admin', roles: ['platform_admin'], activeOrgId: 'org-1' },
  });
});

async function mountDetail(customerId: string) {
  const api = await import('@/connect/customer-api');
  vi.mocked(api.getCustomer).mockResolvedValue({ id: customerId, name: 'Acme', status: 'active' } as never);
  vi.mocked(api.listCustomerEvents).mockResolvedValue([] as never);
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:id', name: 'CustomerDetail', component: CustomerDetailPage },
      { path: '/customers', name: 'CustomerList', component: Stub },
      { path: '/customers/:customerId/clusters', name: 'ClusterList', component: Stub },
      { path: '/', name: 'Home', component: Stub },
    ],
  });
  await router.push(`/customers/${customerId}`);
  await router.isReady();
  const { useCustomerStore } = await import('@/stores/customers');
  useCustomerStore().$patch({ current: { id: customerId, name: 'Acme', status: 'active' } as never });
  const wrapper = mount(CustomerDetailPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

describe('customer detail cluster entry', () => {
  // The page used to offer exactly one link ("Back"), so clusters were unreachable.
  it('links to the customer’s clusters', async () => {
    const wrapper = await mountDetail('c9');

    const link = wrapper.findAll('a').find((a) => a.text() === '集群');
    expect(link, 'no cluster entry on the customer detail page').toBeTruthy();
    expect(link!.attributes('href')).toBe('/customers/c9/clusters');
  });
});

describe('customer list cluster entries', () => {
  it('links every row to that customer’s clusters', async () => {
    const api = await import('@/connect/customer-api');
    vi.mocked(api.listCustomers).mockResolvedValue([
      { id: 'c1', name: 'Acme', status: 'active' },
      { id: 'c2', name: 'Globex', status: 'active' },
    ] as never);

    const { wrapper } = await mountCustomers('/customers');

    expect(wrapper.get('[data-testid="customer-clusters-c1"]').attributes('href')).toBe('/customers/c1/clusters');
    expect(wrapper.get('[data-testid="customer-clusters-c2"]').attributes('href')).toBe('/customers/c2/clusters');
  });
});
