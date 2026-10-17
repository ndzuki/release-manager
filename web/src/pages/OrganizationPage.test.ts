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
 * Unmount before wiping the DOM: AppDialog teleports to <body>, and a page that is
 * never unmounted leaves its dialog behind for the next test's queries (TASK-301).
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
const mockedRemove = vi.mocked(api.removeMember);
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

/*
 * AppDialog teleports to <body> (ADR-029) and VTU's wrapper scoping does not follow
 * it, so the new organization dialogs are queried in the document (the pattern
 * BindingsPage/DefinitionsPage use for teleported overlays).
 */
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

function signIn(roles: string[]): void {
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'me', roles, activeOrgId: 'org-1' },
    organizations: [{ $typeName: 'auth.v1.Organization', id: 'org-1', name: 'Platform', status: 'active' }],
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
  mockedRemove.mockReset().mockResolvedValue(undefined);
  mockedGetOrg.mockReset().mockResolvedValue(orgView({ optimisticVersion: 5n }));
  mockedCreateOrg.mockReset().mockResolvedValue(orgView({ id: 'org-2', name: 'Acme' }));
  mockedDisableOrg.mockReset().mockResolvedValue(orgView({ status: 'disabled' }));
  signIn(['platform_admin']);
});

describe('OrganizationPage', () => {
  it('lists the members of the active organization with their roles', async () => {
    const wrapper = await mountPage();

    expect(mockedList).toHaveBeenCalledWith('org-1');
    expect(wrapper.find('[data-testid="member-row-admin-1"]').text()).toContain('platform_admin');
    expect(wrapper.find('[data-testid="member-row-viewer-1"]').text()).toContain('viewer');
  });

  it('hides every write affordance from a viewer', async () => {
    signIn(['viewer']);
    const wrapper = await mountPage();

    expect(wrapper.findAll('select[id^="role-"]')).toHaveLength(0);
    expect(wrapper.text()).toContain('只读');
    expect(wrapper.find('form').exists()).toBe(false);
  });

  // AddMember is not convergent (re-adding fails), so the page refuses duplicates
  // before spending a request.
  it('refuses a duplicate member locally', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="userId"]').setValue('viewer-1');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('[data-testid="organization-add-error"]').text()).toContain('已是组织成员');
    expect(mockedAdd).not.toHaveBeenCalled();
  });

  it('requires a user id before adding', async () => {
    const wrapper = await mountPage();

    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('[data-testid="organization-add-error"]').text()).toContain('请填写用户 ID');
    expect(mockedAdd).not.toHaveBeenCalled();
  });

  it('changes a role with the row version and reports success', async () => {
    const wrapper = await mountPage();

    await wrapper.get('select[id="role-viewer-1"]').setValue('deployer');
    await flushPromises();

    expect(mockedUpdate).toHaveBeenCalledWith('org-1', 'viewer-1', 'deployer', 1n);
    expect(wrapper.get('[data-testid="organization-notice"]').text()).toContain('viewer-1');
  });

  // A rejected change (e.g. last platform_admin) must not leave the dropdown on the
  // role the server refused.
  it('restores the previous role in the dropdown when the change is refused', async () => {
    mockedUpdate.mockRejectedValue(new ConnectError('no platform admin left', Code.FailedPrecondition));
    const wrapper = await mountPage();

    const select = wrapper.get<HTMLSelectElement>('select[id="role-admin-1"]');
    await select.setValue('viewer');
    await flushPromises();

    expect(wrapper.get('.error-state').text()).toContain('platform_admin');
    expect(wrapper.get<HTMLSelectElement>('select[id="role-admin-1"]').element.value).toBe('platform_admin');
  });

  it('removes a member after confirmation', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="member-row-viewer-1"] button').trigger('click');
    await flushPromises();

    expect(mockedRemove).toHaveBeenCalledWith('org-1', 'viewer-1', 1n);
    expect(mockedList).toHaveBeenCalledTimes(2);
  });

  it('adds a member with the chosen role and reports success', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="userId"]').setValue('new-user');
    await wrapper.get('select[name="role"]').setValue('deployer');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(mockedAdd).toHaveBeenCalledWith('org-1', 'new-user', 'deployer');
    expect(mockedList).toHaveBeenCalledTimes(2);
    expect(wrapper.get('[data-testid="organization-notice"]').text()).toContain('new-user');
  });
});

/*
 * TASK-301: organization-level create/disable had no console surface at all, so the
 * console could not create an organization. Create is platform_admin-only (the
 * handler re-checks the role), disable needs organization/write and is terminal, so
 * it confirms through a danger alertdialog that reads the version it will send.
 *
 * Every negative assertion is paired with a positive one: "disableOrganization was
 * not called" alone would also pass when the dialog never opened.
 */
describe('OrganizationPage organization-level actions (TASK-301)', () => {
  it('creates an organization and reports it', async () => {
    const wrapper = await mountPage();
    mockedCreateOrg.mockResolvedValue(orgView({ id: 'org-2', name: 'Acme' }));

    await wrapper.get('[data-testid="organization-create-open"]').trigger('click');
    expect(createOrgRoot()).toBeTruthy();
    setOrgName('  Acme  ');
    await nextTick();
    await submitCreateOrg();

    expect(mockedCreateOrg).toHaveBeenCalledWith('Acme');
    expect(wrapper.get('[data-testid="organization-org-notice"]').text()).toContain('已创建组织 Acme');
    // The dialog closed and the field was cleared.
    expect(document.querySelector('[data-testid="organization-create-submit"]')).toBeNull();
  });

  it('refuses an empty organization name before sending', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="organization-create-open"]').trigger('click');

    await submitCreateOrg();

    expect(document.querySelector('[data-testid="organization-create-validation"]')).not.toBeNull();
    expect(mockedCreateOrg).not.toHaveBeenCalled();
    expect(createOrgRoot()).toBeTruthy();
  });

  it('keeps the create dialog open and shows the reason inside it', async () => {
    const wrapper = await mountPage();
    mockedCreateOrg.mockRejectedValue(new ConnectError('requires role platform_admin', Code.PermissionDenied));

    await wrapper.get('[data-testid="organization-create-open"]').trigger('click');
    setOrgName('Acme');
    await nextTick();
    await submitCreateOrg();

    const failure = document.querySelectorAll<HTMLElement>('[data-testid="organization-create-error"]');
    expect(failure).toHaveLength(1);
    expect(failure[0]?.textContent?.trim()).toContain('platform_admin');
    expect(createOrgRoot()).toBeTruthy();
  });

  // CreateOrganization is platform_admin-only in the handler, while rename/disable
  // only need `organization/write`: the two buttons must not follow the same role.
  it('offers create only to platform_admin but disable to release_admin', async () => {
    signIn(['release_admin']);
    const wrapper = await mountPage();

    expect(wrapper.find('[data-testid="organization-create-open"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="organization-disable-open"]').exists()).toBe(true);
  });

  it('offers neither action to a viewer, while still rendering the member list', async () => {
    signIn(['viewer']);
    const wrapper = await mountPage();

    // Positive first: the page rendered its normal content, so the two absences below
    // are about the role and not about a page that failed to mount.
    expect(wrapper.get('[data-testid="member-row-admin-1"]').text()).toContain('platform_admin');
    expect(wrapper.find('[data-testid="organization-create-open"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="organization-disable-open"]').exists()).toBe(false);
  });

  it('reads the current organization before arming, and cancelling executes nothing', async () => {
    const wrapper = await mountPage();
    mockedGetOrg.mockResolvedValue(orgView({ name: 'Fresh', optimisticVersion: 9n }));

    await wrapper.get('[data-testid="organization-disable-open"]').trigger('click');
    await flushPromises();

    // The version comes from a fresh read, never from the login-time snapshot.
    expect(mockedGetOrg).toHaveBeenCalledWith('org-1');
    expect(mockedDisableOrg).not.toHaveBeenCalled();
    const dialog = getByRole('alertdialog');
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    const titleId = dialog.getAttribute('aria-labelledby');
    expect(document.getElementById(titleId ?? '')?.textContent).toBe('禁用组织');
    // The description names the organization the fresh read returned.
    expect(dialog.querySelector('.app-dialog__description')?.textContent).toContain('Fresh');

    clickButton(dialog, '取消');
    await flushPromises();

    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
    expect(mockedDisableOrg).not.toHaveBeenCalled();
  });

  it('disables with the freshly read version only after confirming', async () => {
    const wrapper = await mountPage();
    mockedGetOrg.mockResolvedValue(orgView({ name: 'Fresh', optimisticVersion: 9n }));
    mockedDisableOrg.mockResolvedValue(orgView({ name: 'Fresh', status: 'disabled' }));

    await wrapper.get('[data-testid="organization-disable-open"]').trigger('click');
    await flushPromises();
    const dialog = getByRole('alertdialog');
    clickButton(dialog, '确认停用');
    await flushPromises();

    expect(mockedDisableOrg).toHaveBeenCalledTimes(1);
    expect(mockedDisableOrg).toHaveBeenCalledWith('org-1', 9n);
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
    expect(wrapper.get('[data-testid="organization-org-notice"]').text()).toContain('已停用组织 Fresh');
  });

  /*
   * The page-level alert renders behind AppDialog's fixed backdrop, so a refused
   * disable has to put the reason inside the still-open confirmation, exactly once.
   */
  it('keeps the failure reason inside the still-open dialog', async () => {
    const wrapper = await mountPage();
    mockedGetOrg.mockResolvedValue(orgView({ name: 'Fresh', optimisticVersion: 9n }));
    mockedDisableOrg.mockRejectedValue(new ConnectError('optimistic lock conflict', Code.Aborted));

    await wrapper.get('[data-testid="organization-disable-open"]').trigger('click');
    await flushPromises();
    const dialog = getByRole('alertdialog');
    clickButton(dialog, '确认停用');
    await flushPromises();

    expect(document.querySelector('[role="alertdialog"]')).toBe(dialog);
    const inside = dialog.querySelectorAll<HTMLElement>('[data-testid="organization-disable-error"]');
    expect(inside).toHaveLength(1);
    expect(inside[0]?.textContent?.trim()).toContain('已被其他人修改');
    expect(document.querySelectorAll('[data-testid="organization-disable-error"]')).toHaveLength(1);
    // A conflict re-reads the row so the retry carries the version the server holds.
    expect(mockedGetOrg).toHaveBeenCalledTimes(2);
  });

  it('reports an arm failure on the page, without opening a confirmation', async () => {
    const wrapper = await mountPage();
    mockedGetOrg.mockRejectedValue(new ConnectError('organization not found', Code.NotFound));

    await wrapper.get('[data-testid="organization-disable-open"]').trigger('click');
    await flushPromises();

    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
    // Page-level node, so it lives on the wrapper (only dialog content is teleported).
    const failure = wrapper.findAll('[data-testid="organization-disable-arm-error"]');
    expect(failure).toHaveLength(1);
    expect(failure[0]?.text().trim()).toBe('组织不存在（可能已被删除）');
  });

  it('does not offer a second disable for an organization the server reports as disabled', async () => {
    const wrapper = await mountPage();
    mockedGetOrg.mockResolvedValue(orgView({ status: 'disabled' }));

    await wrapper.get('[data-testid="organization-disable-open"]').trigger('click');
    await flushPromises();

    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
    expect(wrapper.get('[data-testid="organization-disable-arm-error"]').text().trim()).toBe(
      '该组织已停用，无法再次停用。',
    );
    expect(mockedDisableOrg).not.toHaveBeenCalled();
  });

  it('associates the organization name field with its label', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="organization-create-open"]').trigger('click');

    const root = createOrgRoot();
    const input = root.querySelector<HTMLInputElement>('input[name="organizationName"]');
    expect(input).not.toBeNull();
    const id = input?.getAttribute('id');
    expect(id).toBeTruthy();
    const label = root.querySelector(`.organization__field label[for="${id}"]`);
    expect(label, `no label resolves to the create input ${id}`).not.toBeNull();
    expect(label?.textContent?.trim()).not.toBe('');
  });
});
