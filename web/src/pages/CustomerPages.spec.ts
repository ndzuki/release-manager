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

    expect(wrapper.text()).toContain('你的只读角色没有新建客户入口。');
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

/*
 * `Customer.status` is a free-form string on the wire. The list page used to render
 * every non-active value as "已停用" and offered Edit on `status === 'active'`; a
 * status the console cannot read now has to read as neither state, because both of
 * the old answers were claims about the tenant that nobody had established.
 */
describe('customer list status pill', () => {
  it('renders a status it cannot read as unknown rather than as a real state', async () => {
    authenticateAdmin();
    const store = useCustomerStore();
    store.loading = false;
    store.customers = [{ id: 'cust-1', name: 'Acme', slug: 'acme', status: 'unknown', version: 1 }];
    store.loadList = vi.fn().mockResolvedValue(undefined);

    const wrapper = await mountAt(CustomerListPage, '/customers');
    await nextTick();

    const pill = wrapper.get('.status');
    expect(pill.text()).toBe('未知');
    expect(pill.classes()).toContain('status--unknown');
    expect(pill.classes()).not.toContain('status--active');
    expect(pill.classes()).not.toContain('status--disabled');
    // Edit is gated on a status the console CAN read, so it must not be offered here.
    expect(wrapper.text()).not.toContain('编辑');
  });

  // Positive control for the 3-way branch above.
  it('still labels the two real states', async () => {
    authenticateAdmin();
    const store = useCustomerStore();
    store.loading = false;
    store.customers = [
      { id: 'cust-1', name: 'Acme', slug: 'acme', status: 'active', version: 1 },
      { id: 'cust-2', name: 'Globex', slug: 'globex', status: 'disabled', version: 1 },
    ];
    store.loadList = vi.fn().mockResolvedValue(undefined);

    const wrapper = await mountAt(CustomerListPage, '/customers');
    await nextTick();

    expect(wrapper.findAll('.status').map((pill) => pill.text())).toEqual(['生效中', '已停用']);
  });
});

/*
 * TASK-334: the detail form's read-only gate used to recognise only `disabled`.
 * `Customer.status` is a free-form wire string, so a status this console could not
 * read left the form editable — and UpdateCustomer does not check the status
 * server-side, so a direct detail URL was a real write entry point for a state
 * nobody had established. Editability is now three-way: active editable, disabled
 * read-only, anything else read-only with an explicit notice.
 */
describe('customer detail edit gate', () => {
  async function mountDetailWithStatus(status: 'active' | 'disabled' | 'unknown') {
    authenticateAdmin();
    const store = useCustomerStore();
    store.loading = false;
    store.current = { id: 'cust-1', name: 'Acme', slug: 'acme', status, version: 1 };
    store.draft = { name: 'Acme', slug: 'acme', version: 1 };
    store.loadCustomer = vi.fn().mockResolvedValue(undefined);
    store.loadHistory = vi.fn().mockResolvedValue(undefined);
    const wrapper = await mountAt(CustomerDetailPage, '/customers/cust-1');
    await nextTick();
    return wrapper;
  }

  // Positive control: the new gate must not make a readable active customer read-only.
  it('keeps an active customer editable', async () => {
    const wrapper = await mountDetailWithStatus('active');

    expect(wrapper.findAll('input').every((input) => input.attributes('disabled') === undefined)).toBe(true);
    expect(wrapper.text()).toContain('保存修改');
    expect(wrapper.find('[data-testid="customer-unknown-status"]').exists()).toBe(false);
  });

  it('keeps a disabled customer read-only with the disabled banner', async () => {
    const wrapper = await mountDetailWithStatus('disabled');

    expect(wrapper.findAll('input').every((input) => input.attributes('disabled') !== undefined)).toBe(true);
    expect(wrapper.text()).not.toContain('保存修改');
    expect(wrapper.text()).toContain('该客户已停用');
  });

  it('renders a status it cannot read as read-only with an explicit notice', async () => {
    const wrapper = await mountDetailWithStatus('unknown');

    expect(wrapper.findAll('input').every((input) => input.attributes('disabled') !== undefined)).toBe(true);
    expect(wrapper.find('form button[type="submit"]').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('保存修改');
    const notice = wrapper.get('[data-testid="customer-unknown-status"]');
    expect(notice.attributes('role')).toBe('status');
    expect(notice.text()).toContain('客户状态无法识别');
  });
});
