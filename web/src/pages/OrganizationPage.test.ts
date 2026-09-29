import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import OrganizationPage from './OrganizationPage.vue';
import { useAuthStore } from '@/stores/auth';
import * as api from '@/connect/organization-api';

vi.mock('@/connect/organization-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return {
    ...original,
    listMembers: vi.fn(),
    addMember: vi.fn(),
    updateMemberRole: vi.fn(),
    removeMember: vi.fn(),
  };
});

const mockedList = vi.mocked(api.listMembers);
const mockedAdd = vi.mocked(api.addMember);
const mockedUpdate = vi.mocked(api.updateMemberRole);
const mockedRemove = vi.mocked(api.removeMember);

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
