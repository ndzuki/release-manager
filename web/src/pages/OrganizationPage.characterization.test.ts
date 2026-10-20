import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import { createMemoryHistory, createRouter } from 'vue-router';
import OrganizationPage from './OrganizationPage.vue';
import { useAuthStore } from '@/stores/auth';
import * as api from '@/connect/organization-api';

/*
 * TASK-363 characterization tests (second part, OrganizationPage).
 *
 * The page was 585 lines and is being split into composables + components. These
 * cases pin behaviour the CURRENT page already has but OrganizationPage.test.ts
 * does not cover: the header identity, the three page-level states, the refresh
 * in-flight label, the grantable-role projection inside both selects, the add-form
 * trim/reset/refusal paths, the create dialog's reset-on-open, the disable read's
 * in-flight label, and — the case #443's review found missing — that a lost
 * disable race makes the retry carry the version the server now holds.
 *
 * They are written against the DOM, not against internals, so they keep holding
 * after the split.
 */
const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

vi.mock('@/connect/organization-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return {
    ...original,
    listMembers: vi.fn(),
    addMember: vi.fn(),
    updateMemberRole: vi.fn(),
    removeMember: vi.fn(),
    getOrganization: vi.fn(),
    createOrganization: vi.fn(),
    disableOrganization: vi.fn(),
  };
});

const mockedList = vi.mocked(api.listMembers);
const mockedAdd = vi.mocked(api.addMember);
const mockedUpdate = vi.mocked(api.updateMemberRole);
const mockedGetOrg = vi.mocked(api.getOrganization);
const mockedCreateOrg = vi.mocked(api.createOrganization);
const mockedDisableOrg = vi.mocked(api.disableOrganization);

function orgView(overrides: Partial<api.OrganizationView> = {}): api.OrganizationView {
  return {
    id: 'org-1',
    name: 'Platform',
    status: 'active',
    optimisticVersion: 5n,
    createdAt: null,
    updatedAt: null,
    ...overrides,
  };
}

function getByRole(role: string): HTMLElement {
  const matches = Array.from(document.querySelectorAll<HTMLElement>(`[role="${role}"]`));
  if (matches.length !== 1) throw new Error(`expected exactly one role="${role}", found ${matches.length}`);
  return matches[0]!;
}

function clickButton(root: HTMLElement, label: string): void {
  const button = Array.from(root.querySelectorAll('button')).find(
    (element) => element.textContent?.trim() === label,
  );
  if (!button) throw new Error(`dialog button not found: ${label}`);
  button.click();
}

function createOrgRoot(): HTMLElement {
  const root = document.querySelector<HTMLElement>('.organization__create');
  if (!root) throw new Error('organization create dialog not rendered');
  return root;
}

function setOrgName(value: string): void {
  const input = document.querySelector<HTMLInputElement>('input[name="organizationName"]');
  if (!input) throw new Error('organization name input not rendered');
  input.value = value;
  input.dispatchEvent(new Event('input'));
}

async function submitCreateOrg(): Promise<void> {
  const form = createOrgRoot().querySelector('form');
  if (!form) throw new Error('organization create form not rendered');
  form.dispatchEvent(new Event('submit'));
  await flushPromises();
}

function signIn(roles: string[], activeOrgId = 'org-1'): void {
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'me', roles, activeOrgId },
    organizations: activeOrgId
      ? [{ $typeName: 'auth.v1.Organization', id: 'org-1', name: 'Platform', status: 'active' }]
      : [],
  });
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/settings/organization', name: 'OrganizationMembers', component: OrganizationPage }],
  });
  await router.push('/settings/organization');
  await router.isReady();
  const wrapper = mount(OrganizationPage, { global: { plugins: [router] } });
  mounted.push(wrapper);
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue([
    { userId: 'admin-1', role: 'platform_admin', optimisticVersion: 3n, createdAt: '2026-09-28T00:00:00Z', updatedAt: null },
    { userId: 'viewer-1', role: 'viewer', optimisticVersion: 1n, createdAt: null, updatedAt: null },
  ]);
  mockedAdd.mockReset().mockResolvedValue(undefined);
  mockedUpdate.mockReset().mockResolvedValue(undefined);
  mockedGetOrg.mockReset().mockResolvedValue(orgView({ optimisticVersion: 5n }));
  mockedCreateOrg.mockReset().mockResolvedValue(orgView({ id: 'org-2', name: 'Acme' }));
  mockedDisableOrg.mockReset().mockResolvedValue(orgView({ status: 'disabled' }));
  signIn(['platform_admin']);
});

describe('OrganizationPage header and page-level states (TASK-363)', () => {
  it('renders the active organization name in the header without printing its UUID', async () => {
    const wrapper = await mountPage();

    expect(wrapper.get('h1').text()).toBe('组织成员');
    expect(wrapper.get('.page-header__eyebrow').text()).toBe('治理');
    const subtitle = wrapper.get('.page-header__subtitle');
    expect(subtitle.text()).toBe('Platform');
    // TASK-394: the subtitle used to print the organization UUID in a <code> element;
    // the name the store carries replaces it, so no identifier is rendered here.
    expect(subtitle.text()).not.toContain('org-1');
    expect(subtitle.find('code').exists()).toBe(false);
  });

  it('reports the in-flight refresh in the button label', async () => {
    const wrapper = await mountPage();
    let resolveList: ((value: api.OrganizationMemberView[]) => void) | undefined;
    mockedList.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveList = resolve;
        }),
    );

    const refresh = wrapper.findAll('button').find((button) => button.text().trim() === '刷新');
    if (!refresh) throw new Error('refresh button not rendered');
    await refresh.trigger('click');
    await nextTick();

    const busy = wrapper.findAll('button').find((button) => button.text().trim() === '刷新中…');
    expect(busy).toBeTruthy();
    expect((busy?.element as HTMLButtonElement).disabled).toBe(true);

    resolveList?.([
      { userId: 'admin-1', role: 'platform_admin', optimisticVersion: 3n, createdAt: null, updatedAt: null },
    ]);
    await flushPromises();

    const idle = wrapper.findAll('button').find((button) => button.text().trim() === '刷新');
    expect(idle?.element.disabled).toBe(false);
  });

  it('renders the empty state when the organization has no members', async () => {
    mockedList.mockResolvedValue([]);
    const wrapper = await mountPage();

    expect(wrapper.get('.empty-state').text()).toContain('暂无成员');
    expect(wrapper.find('.organization__table').exists()).toBe(false);
  });

  it('renders the loading state only while the first read is in flight', async () => {
    let resolveList: ((value: api.OrganizationMemberView[]) => void) | undefined;
    mockedList.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveList = resolve;
        }),
    );
    const wrapper = await mountPage();

    expect(wrapper.get('.loading-state').text()).toContain('正在加载成员…');

    resolveList?.([
      { userId: 'admin-1', role: 'platform_admin', optimisticVersion: 3n, createdAt: null, updatedAt: null },
    ]);
    await flushPromises();

    expect(wrapper.find('.loading-state').exists()).toBe(false);
    expect(wrapper.find('.organization__table').exists()).toBe(true);
  });

  it('renders the mapped failure and retries through the ErrorState action', async () => {
    mockedList.mockRejectedValueOnce(new ConnectError('backend down', Code.Unavailable));
    const wrapper = await mountPage();

    const error = wrapper.get('.error-state');
    expect(error.text()).toContain('成员操作未成功');
    expect(error.text()).toContain('操作失败，请稍后重试');

    await error.get('.error-state__action').trigger('click');
    await flushPromises();

    expect(mockedList).toHaveBeenCalledTimes(2);
    expect(wrapper.find('.error-state').exists()).toBe(false);
    expect(wrapper.get('[data-testid="member-row-admin-1"]').text()).toContain('platform_admin');
  });

  // A non-retryable refusal (the last platform_admin) must not offer a retry that
  // would be refused again; the page passes an empty action label for it.
  it('offers no retry action for a non-retryable failure', async () => {
    mockedList.mockRejectedValueOnce(new ConnectError('last admin', Code.FailedPrecondition));
    const wrapper = await mountPage();

    expect(wrapper.get('.error-state').text()).toContain('不能移除或降级最后一个 platform_admin');
    expect(wrapper.find('.error-state__action').exists()).toBe(false);
  });

  it('does not offer disable for an organization the session already reports as disabled', async () => {
    useAuthStore().$patch({
      status: 'authenticated',
      initialized: true,
      user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'me', roles: ['platform_admin'], activeOrgId: 'org-1' },
      organizations: [{ $typeName: 'auth.v1.Organization', id: 'org-1', name: 'Platform', status: 'disabled' }],
    });
    const wrapper = await mountPage();

    // Positive first: the page rendered its normal content, so the absence below
    // is about the status and not about a page that failed to mount.
    expect(wrapper.find('[data-testid="member-row-admin-1"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="organization-disable-open"]').exists()).toBe(false);
  });
});

describe('OrganizationPage member table projection (TASK-363)', () => {
  it('renders the version, the join time and the role pill for each row', async () => {
    const wrapper = await mountPage();

    const cells = wrapper.get('[data-testid="member-row-admin-1"]').findAll('td');
    expect(cells[1]?.get('.organization__role--platform_admin').text()).toBe('platform_admin');
    expect(cells[2]?.text()).toBe('3');
    // The null createdAt keeps the shared formatter's em-dash fallback.
    expect(cells[3]?.text()).not.toBe('');
    expect(wrapper.get('[data-testid="member-row-viewer-1"]').findAll('td')[3]?.text()).toBe('—');
  });

  it('disables the role options the caller cannot grant', async () => {
    signIn(['release_admin']);
    const wrapper = await mountPage();

    const select = wrapper.get<HTMLSelectElement>('select[id="role-viewer-1"]');
    expect(select.findAll('option').map((option) => option.element.value)).toEqual([
      'platform_admin',
      'release_admin',
      'deployer',
      'viewer',
    ]);
    expect(select.get<HTMLOptionElement>('option[value="platform_admin"]').element.disabled).toBe(true);
    expect(select.get<HTMLOptionElement>('option[value="deployer"]').element.disabled).toBe(false);
  });

  it('lists only the grantable roles in the add form', async () => {
    signIn(['release_admin']);
    const wrapper = await mountPage();

    expect(wrapper.get<HTMLSelectElement>('select[name="role"]').findAll('option').map((option) => option.element.value)).toEqual([
      'release_admin',
      'deployer',
      'viewer',
    ]);
  });

  it('marks every row read-only for a caller that can grant nothing', async () => {
    signIn(['viewer']);
    const wrapper = await mountPage();

    const markers = wrapper.findAll('.organization__readonly');
    expect(markers).toHaveLength(2);
    expect(markers.every((marker) => marker.text() === '只读')).toBe(true);
  });
});

describe('OrganizationPage add-member form (TASK-363)', () => {
  it('trims the user id before adding', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="userId"]').setValue('  new-user  ');
    await wrapper.get('select[name="role"]').setValue('deployer');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(mockedAdd).toHaveBeenCalledWith('org-1', 'new-user', 'deployer');
  });

  it('clears the user id but keeps the chosen role after a successful add', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="userId"]').setValue('new-user');
    await wrapper.get('select[name="role"]').setValue('deployer');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.get<HTMLInputElement>('input[name="userId"]').element.value).toBe('');
    expect(wrapper.get<HTMLSelectElement>('select[name="role"]').element.value).toBe('deployer');
    // The membership notice is a status line, not an alert.
    expect(wrapper.get('[data-testid="organization-notice"]').attributes('role')).toBe('status');
  });

  it('refuses a duplicate written with surrounding whitespace', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="userId"]').setValue('  viewer-1  ');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('[data-testid="organization-add-error"]').text()).toContain('已是组织成员');
    expect(mockedAdd).not.toHaveBeenCalled();
  });

  it('reports a refused add through the page-level error state and keeps the typed id', async () => {
    mockedAdd.mockRejectedValueOnce(new ConnectError('duplicate member', Code.AlreadyExists));
    const wrapper = await mountPage();

    await wrapper.get('input[name="userId"]').setValue('new-user');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('.error-state').text()).toContain('该用户已是组织成员；请直接修改其角色');
    expect(wrapper.get<HTMLInputElement>('input[name="userId"]').element.value).toBe('new-user');
  });
});

describe('OrganizationPage create dialog (TASK-363)', () => {
  it('resets the name and the validation message when the dialog is reopened', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="organization-create-open"]').trigger('click');
    await submitCreateOrg();
    expect(document.querySelector('[data-testid="organization-create-validation"]')).not.toBeNull();
    setOrgName('Acme');
    await nextTick();
    clickButton(createOrgRoot(), '取消');
    await nextTick();
    expect(document.querySelector('.organization__create')).toBeNull();
    expect(mockedCreateOrg).not.toHaveBeenCalled();

    await wrapper.get('[data-testid="organization-create-open"]').trigger('click');

    expect(document.querySelector<HTMLInputElement>('input[name="organizationName"]')?.value).toBe('');
    expect(document.querySelector('[data-testid="organization-create-validation"]')).toBeNull();
  });
});

describe('OrganizationPage disable confirmation (TASK-363)', () => {
  it('reports the in-flight fresh read in the header button', async () => {
    let resolveRead: ((value: api.OrganizationView) => void) | undefined;
    mockedGetOrg.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveRead = resolve;
        }),
    );
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="organization-disable-open"]').trigger('click');
    await nextTick();

    const busy = wrapper.get('[data-testid="organization-disable-open"]');
    expect(busy.text()).toBe('停用中…');
    expect((busy.element as HTMLButtonElement).disabled).toBe(true);

    resolveRead?.(orgView());
    await flushPromises();
    expect(getByRole('alertdialog')).toBeTruthy();
  });

  it('reports a missing active organization without calling the server', async () => {
    signIn(['platform_admin'], '');
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="organization-disable-open"]').trigger('click');
    await flushPromises();

    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
    expect(mockedGetOrg).not.toHaveBeenCalled();
    expect(wrapper.get('[data-testid="organization-disable-arm-error"]').text()).toBe(
      '当前没有可停用的组织（会话未返回活动组织）。',
    );
  });

  /*
   * The existing page test asserts only that a conflict re-reads the row. What
   * keeps the hinted retry useful is that the confirmation ADOPTS the version the
   * re-read returned: a page that merely reloads the list would send the refused
   * version again and fail forever. This case makes the second read move to 10 and
   * checks the retry's payload.
   */
  it('adopts the version the server holds after a conflict so the retry carries it', async () => {
    mockedGetOrg
      .mockResolvedValueOnce(orgView({ name: 'Fresh', optimisticVersion: 9n }))
      .mockResolvedValueOnce(orgView({ name: 'Fresh', optimisticVersion: 10n }));
    mockedDisableOrg.mockRejectedValueOnce(new ConnectError('optimistic lock conflict', Code.Aborted));
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="organization-disable-open"]').trigger('click');
    await flushPromises();
    const dialog = getByRole('alertdialog');
    clickButton(dialog, '确认停用');
    await flushPromises();

    expect(mockedDisableOrg).toHaveBeenLastCalledWith('org-1', 9n);
    expect(mockedGetOrg).toHaveBeenCalledTimes(2);
    expect(dialog.querySelector('[data-testid="organization-disable-error"]')).not.toBeNull();

    mockedDisableOrg.mockResolvedValueOnce(orgView({ name: 'Fresh', status: 'disabled' }));
    clickButton(dialog, '确认停用');
    await flushPromises();

    expect(mockedDisableOrg).toHaveBeenLastCalledWith('org-1', 10n);
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
  });
});

describe('OrganizationPage role change (TASK-363)', () => {
  // The refused select value is restored by the page; the store reloads the list
  // first, so the row the user sees may already carry the server's role.
  it('keeps the dropdown on the server role after a refusal', async () => {
    mockedUpdate.mockRejectedValueOnce(new ConnectError('no platform admin left', Code.FailedPrecondition));
    const wrapper = await mountPage();

    await wrapper.get('select[id="role-admin-1"]').setValue('viewer');
    await flushPromises();

    expect(wrapper.get<HTMLSelectElement>('select[id="role-admin-1"]').element.value).toBe('platform_admin');
    expect(wrapper.get('.error-state').text()).toContain('platform_admin');
  });
});
