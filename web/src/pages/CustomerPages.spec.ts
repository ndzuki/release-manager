import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import { mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import CustomerListPage from './CustomerListPage.vue';
import CustomerDetailPage from './CustomerDetailPage.vue';
import DisableCustomerDialog from '@/components/customers/DisableCustomerDialog.vue';
import { createAppRouter } from '@/router';
import { useAuthStore } from '@/stores/auth';
import { useCustomerStore } from '@/stores/customers';

async function mountAt(component: typeof CustomerListPage | typeof CustomerDetailPage, path: string) {
  const router = createAppRouter();
  await router.push(path);
  await router.isReady();
  return mount(component, {
    global: {
      plugins: [router],
      stubs: { RouterLink: false },
    },
  });
}

function authenticateViewer() {
  const auth = useAuthStore();
  auth.status = 'authenticated';
  auth.initialized = true;
  auth.user = {
    id: 'viewer',
    username: 'viewer',
    roles: ['viewer'],
    activeOrgId: 'org-1',
  } as never;
}

function authenticateAdmin() {
  const auth = useAuthStore();
  auth.status = 'authenticated';
  auth.initialized = true;
  auth.user = {
    id: 'admin',
    username: 'admin',
    roles: ['platform_admin'],
    activeOrgId: 'org-1',
  } as never;
}

beforeEach(() => {
  setActivePinia(createPinia());
  vi.clearAllMocks();
});

describe('customer viewer boundaries', () => {
  it('shows no create or edit entry on the list page', async () => {
    authenticateViewer();
    const store = useCustomerStore();
    store.loading = false;
    store.customers = [{ id: 'cust-1', name: 'Acme', slug: 'acme', status: 'active', version: 1 }];
    store.loadList = vi.fn().mockResolvedValue(undefined);

    const wrapper = await mountAt(CustomerListPage, '/customers');
    await nextTick();

    expect(wrapper.text()).not.toContain('Create customer');
    expect(wrapper.text()).not.toContain('Edit');
    expect(wrapper.text()).toContain('View');
  });

  it('shows no writable form or disable action on an existing customer', async () => {
    authenticateViewer();
    const store = useCustomerStore();
    store.loading = false;
    store.current = { id: 'cust-1', name: 'Acme', slug: 'acme', status: 'active', version: 1 };
    store.draft = { name: 'Acme', slug: 'acme', version: 1 };
    store.loadCustomer = vi.fn().mockResolvedValue(undefined);
    store.loadHistory = vi.fn().mockResolvedValue(undefined);

    const wrapper = await mountAt(CustomerDetailPage, '/customers/cust-1');
    await nextTick();

    expect(wrapper.text()).not.toContain('Save changes');
    expect(wrapper.text()).not.toContain('Disable customer');
    expect(wrapper.text()).toContain('Read-only access');
  });

  it('shows no customer creation entry on /customers/new', async () => {
    authenticateViewer();
    const store = useCustomerStore();
    store.startCreate = vi.fn(() => { store.draft = { name: '', slug: '', version: 0 }; });

    const wrapper = await mountAt(CustomerDetailPage, '/customers/new');
    await nextTick();

    expect(wrapper.text()).toContain('Your viewer role has no customer creation entry.');
    expect(wrapper.find('form').exists()).toBe(false);
  });
});

describe('disable confirmation', () => {
  it('shows cascade risks and locks confirmation while pending', async () => {
    const wrapper = mount(DisableCustomerDialog, { props: { open: true, pending: true } });

    expect(wrapper.text()).toContain('Enrollment tokens');
    expect(wrapper.text()).toContain('Operator certificates');
    expect(wrapper.text()).toContain('Active Operator Sessions');
    expect(wrapper.get('input[type="checkbox"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('.danger').attributes('disabled')).toBeDefined();
  });
});

describe('customer detail route reuse', () => {
  it('reloads detail and history when the create form redirects to the new customer', async () => {
    authenticateAdmin();
    const store = useCustomerStore();
    store.startCreate = vi.fn(() => { store.draft = { name: '', slug: '', version: 0 }; });
    store.loadCustomer = vi.fn().mockResolvedValue(undefined);
    store.loadHistory = vi.fn().mockResolvedValue(undefined);

    const router = createAppRouter();
    await router.push('/customers/new');
    await router.isReady();
    mount(CustomerDetailPage, { global: { plugins: [router], stubs: { RouterLink: false } } });
    await nextTick();

    // CustomerNew and CustomerDetail share one component; the redirect after
    // create reuses the instance, so the page must react to the route change.
    await router.replace({ name: 'CustomerDetail', params: { id: 'new-1' } });
    await nextTick();
    await nextTick();

    expect(store.loadCustomer).toHaveBeenCalledWith('new-1');
    expect(store.loadHistory).toHaveBeenCalledWith('new-1');
  });
});
