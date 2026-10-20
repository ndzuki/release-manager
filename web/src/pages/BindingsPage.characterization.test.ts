import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import BindingsPage from './BindingsPage.vue';
import { useAuthStore } from '@/stores/auth';
import * as api from '@/connect/binding-api';

/*
 * TASK-363 characterization tests (third part, BindingsPage).
 *
 * The page is 447 lines and is being split into composables + components. These
 * cases pin behaviour the CURRENT page already has but BindingsPage.test.ts does
 * not cover: the header identity and its no-active-org fallback, the loading /
 * empty / error states, the in-flight labels and disabled writes, the row values
 * and status classes, the create and grant forms' trim / reset / refusal paths,
 * the retry affordance, and the confirmation's re-arm and in-flight Escape.
 *
 * They are written against the DOM, not against internals, so they keep holding
 * after the split.
 */
const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

vi.mock('@/connect/binding-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return {
    ...original,
    listBindings: vi.fn(),
    createBinding: vi.fn(),
    revokeBinding: vi.fn(),
    setCapabilityGrant: vi.fn(),
  };
});

const mockedList = vi.mocked(api.listBindings);
const mockedCreate = vi.mocked(api.createBinding);
const mockedRevoke = vi.mocked(api.revokeBinding);
const mockedGrant = vi.mocked(api.setCapabilityGrant);

const CUSTOMER_A_ACTIVE = {
  id: 'b1',
  orgId: 'org-1',
  customerId: 'customer-a',
  status: 'active' as const,
  optimisticVersion: 2n,
  createdAt: '2026-09-28T00:00:00Z',
  updatedAt: null,
};
const CUSTOMER_B_REVOKED = {
  id: 'b2',
  orgId: 'org-1',
  customerId: 'customer-b',
  status: 'revoked' as const,
  optimisticVersion: 4n,
  createdAt: null,
  updatedAt: null,
};

function signIn(activeOrgId: string | null = 'org-1'): void {
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'me', roles: ['platform_admin'], activeOrgId: activeOrgId ?? '' },
    organizations: activeOrgId
      ? [{ $typeName: 'auth.v1.Organization', id: activeOrgId, name: 'Platform', status: 'active' }]
      : [],
  });
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/settings/bindings', name: 'GovernanceBindings', component: BindingsPage }],
  });
  await router.push('/settings/bindings');
  await router.isReady();
  const wrapper = mount(BindingsPage, { global: { plugins: [router] } });
  mounted.push(wrapper);
  await flushPromises();
  return wrapper;
}

function clickButton(root: ParentNode, label: string): void {
  const button = Array.from(root.querySelectorAll('button')).find((element) => element.textContent?.trim() === label);
  if (!button) throw new Error(`button not found: ${label}`);
  button.click();
}

function getByRole(role: string): HTMLElement {
  const matches = Array.from(document.querySelectorAll<HTMLElement>(`[role="${role}"]`));
  if (matches.length !== 1) throw new Error(`expected exactly one role="${role}", found ${matches.length}`);
  return matches[0]!;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue([CUSTOMER_A_ACTIVE, CUSTOMER_B_REVOKED]);
  mockedCreate.mockReset().mockResolvedValue(undefined);
  mockedRevoke.mockReset().mockResolvedValue(undefined);
  mockedGrant.mockReset().mockResolvedValue({ sourceVersion: 1n, policyVersion: 2n });
  signIn();
});

describe('BindingsPage characterization', () => {
  it('renders the governance header with the active organization name and no UUID', async () => {
    const wrapper = await mountPage();

    expect(wrapper.get('.page-header__eyebrow').text()).toBe('治理');
    expect(wrapper.get('h1').text()).toBe('授权绑定');
    const subtitle = wrapper.get('.page-header__subtitle');
    expect(subtitle.text()).toBe('Platform');
    // TASK-394: the subtitle used to print the organization UUID in a <code> element;
    // the name the store carries replaces it, so no identifier is rendered here.
    expect(subtitle.text()).not.toContain('org-1');
    expect(subtitle.find('code').exists()).toBe(false);
  });

  it('loads the active organization on mount', async () => {
    await mountPage();

    expect(mockedList).toHaveBeenCalledTimes(1);
    expect(mockedList).toHaveBeenCalledWith('org-1');
  });

  it('falls back to 当前组织 without an active organization and refreshes with the empty id', async () => {
    signIn(null);
    const wrapper = await mountPage();

    expect(mockedList).not.toHaveBeenCalled();
    expect(wrapper.get('.page-header__subtitle').text()).toBe('当前组织');
    expect(wrapper.find('.page-header__subtitle code').exists()).toBe(false);

    clickButton(wrapper.element as HTMLElement, '刷新');
    await flushPromises();

    expect(mockedList).toHaveBeenCalledWith('');
  });

  it('shows the loading state and the in-flight refresh label', async () => {
    const pending = deferred<api.BindingView[]>();
    mockedList.mockReturnValue(pending.promise);

    const wrapper = await mountPage();

    expect(wrapper.get('.loading-state').text()).toContain('正在加载绑定…');
    const refresh = Array.from((wrapper.element as HTMLElement).querySelectorAll<HTMLElement>('button')).find(
      (element) => element.textContent?.trim() === '刷新中…',
    );
    expect(refresh).toBeDefined();
    expect((refresh as HTMLButtonElement).disabled).toBe(true);
    expect(wrapper.find('table').exists()).toBe(false);

    pending.resolve([CUSTOMER_A_ACTIVE, CUSTOMER_B_REVOKED]);
    await flushPromises();
    expect(wrapper.find('table').exists()).toBe(true);
  });

  it('shows the empty state when the organization has no bindings', async () => {
    mockedList.mockResolvedValue([]);
    const wrapper = await mountPage();

    expect(wrapper.get('.empty-state').text()).toContain('暂无绑定');
    expect(wrapper.get('.empty-state').text()).toContain('该组织还没有与任何客户建立授权绑定。');
    expect(wrapper.find('table').exists()).toBe(false);
  });

  it("renders the columns, versions, timestamps and each row's status class", async () => {
    const wrapper = await mountPage();

    const headers = Array.from((wrapper.element as HTMLElement).querySelectorAll<HTMLElement>('th')).map((th) => th.textContent?.trim());
    expect(headers).toEqual(['客户', '状态', '版本', '建立时间', '操作']);

    const active = wrapper.get('[data-testid="binding-row-customer-a"]');
    expect(active.get('code').text()).toBe('customer-a');
    expect(active.text()).toContain('生效中');
    expect(active.text()).toContain('2');
    expect(active.text()).toMatch(/2026/);
    expect(active.get('.governance__status').classes()).toContain('governance__status--active');

    const revoked = wrapper.get('[data-testid="binding-row-customer-b"]');
    expect(revoked.text()).toContain('已撤销');
    expect(revoked.get('.governance__status').classes()).toContain('governance__status--revoked');
    expect(revoked.get('td:last-child').text()).toBe('—');
    expect(revoked.get('td:last-child span').classes()).toContain('governance__muted');
  });

  it('trims the customer id, clears the input and reports the store notice', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="customerId"]').setValue('  customer-c  ');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(mockedCreate).toHaveBeenCalledWith('org-1', 'customer-c');
    expect((wrapper.get('input[name="customerId"]').element as HTMLInputElement).value).toBe('');
    const notice = wrapper.get('[data-testid="governance-notice"]');
    expect(notice.text()).toBe('已绑定客户 customer-c');
    expect(notice.attributes('role')).toBe('status');
    expect(notice.attributes('tabindex')).toBe('-1');
  });

  it('keeps the typed customer id when the bind is refused', async () => {
    mockedCreate.mockRejectedValue(new ConnectError('bind failed', Code.Unavailable));
    const wrapper = await mountPage();

    await wrapper.get('input[name="customerId"]').setValue('customer-c');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect((wrapper.get('input[name="customerId"]').element as HTMLInputElement).value).toBe('customer-c');
    expect(wrapper.get('.error-state').text()).toContain('操作失败，请稍后重试');
  });

  it('refuses an already active customer even with surrounding whitespace', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="customerId"]').setValue('  customer-a  ');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('[data-testid="binding-create-error"]').text()).toBe('该客户已绑定（正在生效）');
    expect(mockedCreate).not.toHaveBeenCalled();
  });

  // The local guard covers ACTIVE rows only: a revoked pair is exactly what
  // CreateBinding reactivates, so the page must let that request through.
  it('lets a revoked customer through to the server', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="customerId"]').setValue('customer-b');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(mockedCreate).toHaveBeenCalledWith('org-1', 'customer-b');
  });

  it('clears a previous create error when a later bind is submitted', async () => {
    const wrapper = await mountPage();

    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[data-testid="binding-create-error"]').exists()).toBe(true);

    await wrapper.get('input[name="customerId"]').setValue('customer-c');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.find('[data-testid="binding-create-error"]').exists()).toBe(false);
    expect(mockedCreate).toHaveBeenCalledWith('org-1', 'customer-c');
  });

  it('trims the grant subject, clears it and shows the returned versions', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="subject"]').setValue('  user-9  ');
    await wrapper.get('form.governance__panel:last-of-type').trigger('submit');
    await flushPromises();

    expect(mockedGrant).toHaveBeenCalledWith('org-1', 'user-9', 'release.emergency.resolve', false);
    expect((wrapper.get('input[name="subject"]').element as HTMLInputElement).value).toBe('');
    expect(wrapper.get('[data-testid="grant-versions"]').text()).toBe('来源 / 策略 1 / 2');
  });

  it('sends revoked=true when the desired state is 撤销', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="subject"]').setValue('user-9');
    await wrapper.get('select[name="desiredState"]').setValue('revoke');
    await wrapper.get('form.governance__panel:last-of-type').trigger('submit');
    await flushPromises();

    expect(mockedGrant).toHaveBeenCalledWith('org-1', 'user-9', 'release.emergency.resolve', true);
  });

  it('keeps the subject and shows the page failure when a grant is refused', async () => {
    mockedGrant.mockRejectedValue(new ConnectError('grant failed', Code.Unavailable));
    const wrapper = await mountPage();

    await wrapper.get('input[name="subject"]').setValue('user-9');
    await wrapper.get('form.governance__panel:last-of-type').trigger('submit');
    await flushPromises();

    expect((wrapper.get('input[name="subject"]').element as HTMLInputElement).value).toBe('user-9');
    expect(wrapper.find('[data-testid="grant-error"]').exists()).toBe(false);
    expect(wrapper.get('.error-state').text()).toContain('操作失败，请稍后重试');
  });

  it('refreshes the list and clears the notice from the header button', async () => {
    const wrapper = await mountPage();
    await wrapper.get('input[name="customerId"]').setValue('customer-c');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[data-testid="governance-notice"]').exists()).toBe(true);

    clickButton(wrapper.element as HTMLElement, '刷新');
    await flushPromises();

    expect(mockedList).toHaveBeenCalledTimes(3);
    expect(wrapper.find('[data-testid="governance-notice"]').exists()).toBe(false);
  });

  it('offers the retry action on a retryable failure and reloads on click', async () => {
    mockedList.mockRejectedValueOnce(new ConnectError('list failed', Code.Unavailable));
    const wrapper = await mountPage();

    expect(wrapper.get('.error-state').text()).toContain('操作失败，请稍后重试');
    const retry = wrapper.get('.error-state__action');
    expect(retry.text()).toBe('重试');

    await retry.trigger('click');
    await flushPromises();

    expect(mockedList).toHaveBeenCalledTimes(2);
    expect(wrapper.find('.error-state').exists()).toBe(false);
    expect(wrapper.find('table').exists()).toBe(true);
  });

  it('disables every write affordance and shows the in-flight submit labels while saving', async () => {
    const pending = deferred<void>();
    mockedCreate.mockReturnValue(pending.promise);
    const wrapper = await mountPage();

    await wrapper.get('input[name="customerId"]').setValue('customer-c');
    await wrapper.get('form').trigger('submit');

    expect((wrapper.get('[data-testid="binding-row-customer-a"] button').element as HTMLButtonElement).disabled).toBe(true);
    const submitLabels = Array.from((wrapper.element as HTMLElement).querySelectorAll<HTMLButtonElement>('button[type="submit"]')).map((button) =>
      button.textContent?.trim(),
    );
    expect(submitLabels).toEqual(['提交中…', '提交中…']);

    pending.resolve();
    await flushPromises();
    expect(wrapper.find('table').exists()).toBe(true);
  });

  it('does not inherit the previous revoke reason when the confirmation is re-armed', async () => {
    mockedRevoke.mockRejectedValue(new ConnectError('revoke failed', Code.Internal));
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');
    clickButton(getByRole('alertdialog'), '确认撤销');
    await flushPromises();
    expect(document.querySelector('[data-testid="revoke-failure"]')).not.toBeNull();

    clickButton(getByRole('alertdialog'), '取消');
    await flushPromises();
    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');
    await flushPromises();

    expect(document.querySelector('[role="alertdialog"]')).not.toBeNull();
    expect(document.querySelector('[data-testid="revoke-failure"]')).toBeNull();
  });

  it('keeps the confirmation open on Escape while the revoke is in flight', async () => {
    const pending = deferred<void>();
    mockedRevoke.mockReturnValue(pending.promise);
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');
    const dialog = getByRole('alertdialog');
    clickButton(dialog, '确认撤销');
    await flushPromises();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();

    expect(document.querySelector('[role="alertdialog"]')).toBe(dialog);

    pending.resolve();
    await flushPromises();
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
  });
});
