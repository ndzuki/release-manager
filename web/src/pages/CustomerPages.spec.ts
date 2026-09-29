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

    expect(wrapper.text()).not.toContain('新建客户');
    expect(wrapper.text()).not.toContain('编辑');
    expect(wrapper.text()).toContain('查看');
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

    expect(wrapper.text()).not.toContain('保存修改');
    expect(wrapper.text()).not.toContain('停用客户');
    expect(wrapper.text()).toContain('只读访问');
  });

  it('shows no customer creation entry on /customers/new', async () => {
    authenticateViewer();
    const store = useCustomerStore();
    store.startCreate = vi.fn(() => { store.draft = { name: '', slug: '', version: 0 }; });

    const wrapper = await mountAt(CustomerDetailPage, '/customers/new');
    await nextTick();

    expect(wrapper.text()).toContain('你的 viewer 角色没有新建客户入口。');
    expect(wrapper.find('form').exists()).toBe(false);
  });
});

describe('disable confirmation', () => {
  it('shows cascade risks and locks confirmation while pending', async () => {
    // The dialog composes AppDialog, which teleports the overlay to body, so
    // asserting on wrapper.text() would read an empty component tree. Query the
    // rendered document instead (TASK-177).
    const wrapper = mount(DisableCustomerDialog, {
      attachTo: document.body,
      props: { open: true, pending: true },
    });
    await nextTick();

    const panel = document.querySelector<HTMLElement>('.app-dialog__panel');
    expect(panel).not.toBeNull();
    expect(panel?.textContent).toContain('注册令牌');
    expect(panel?.textContent).toContain('Operator 证书');
    expect(panel?.textContent).toContain('活跃的 Operator 会话');
    expect(panel?.querySelector('input[type="checkbox"]')?.hasAttribute('disabled')).toBe(true);
    expect(panel?.querySelector('.disable-dialog__danger')?.hasAttribute('disabled')).toBe(true);

    wrapper.unmount();
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
