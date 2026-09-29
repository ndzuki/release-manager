import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import LocalUsersPage from './LocalUsersPage.vue';
import * as api from '@/connect/local-user-api';
import { useAuthStore } from '@/stores/auth';

/*
 * Local accounts (REQ-025 / A4's second half). The server marks the whole namespace
 * adminOnly, so the page must refuse non-admins itself instead of rendering controls
 * that can only answer 403.
 */
vi.mock('@/connect/local-user-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listLocalUsers: vi.fn(), createLocalUser: vi.fn() };
});

const mockedList = vi.mocked(api.listLocalUsers);
const mockedCreate = vi.mocked(api.createLocalUser);

function signIn(roles: string[]): void {
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-admin', roles, activeOrgId: 'org-1' },
  });
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/settings/users', name: 'LocalUsers', component: LocalUsersPage },
      { path: '/', name: 'Home', component: { template: '<div />' } },
    ],
  });
  await router.push('/settings/users');
  await router.isReady();
  const wrapper = mount(LocalUsersPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
  vi.clearAllMocks();
});

describe('LocalUsersPage', () => {
  it('lists the accounts the server returned', async () => {
    signIn(['platform_admin']);
    mockedList.mockResolvedValue({
      users: [
        { id: 'u1', username: 'alice', roles: ['release_admin'], orgId: 'org-1', status: 'active' },
        { id: 'u2', username: 'bob', roles: [], orgId: 'org-1', status: 'disabled' },
      ],
      nextCursor: '',
    });

    const wrapper = await mountPage();

    expect(wrapper.get('[data-testid="local-user-alice"]').text()).toContain('alice');
    expect(wrapper.get('[data-testid="local-user-alice"]').text()).toContain('发布管理员');
    expect(wrapper.get('[data-testid="local-user-bob"]').text()).toContain('已停用');
  });

  // An existing admin account must not read as a raw wire value next to Chinese roles.
  it('labels an existing platform_admin account', async () => {
    signIn(['platform_admin']);
    mockedList.mockResolvedValue({
      users: [{ id: 'u0', username: 'root', roles: ['platform_admin'], orgId: 'org-1', status: 'active' }],
      nextCursor: '',
    });

    const wrapper = await mountPage();

    expect(wrapper.get('[data-testid="local-user-root"]').text()).toContain('平台管理员');
  });

  it('creates an account and reports it', async () => {
    signIn(['platform_admin']);
    mockedList.mockResolvedValue({ users: [], nextCursor: '' });
    mockedCreate.mockResolvedValue({ id: 'u3', username: 'carol', roles: ['viewer'], orgId: 'org-1', status: 'active' });

    const wrapper = await mountPage();
    await wrapper.get('input[name="username"]').setValue('carol');
    await wrapper.get('input[name="password"]').setValue('s3cret-value');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(mockedCreate).toHaveBeenCalledWith({ username: 'carol', password: 's3cret-value', role: 'viewer' });
    expect(wrapper.get('[data-testid="local-users-notice"]').text()).toContain('carol');
    // D-13: the server reuses an existing account, so the copy says ready, not created.
    expect(wrapper.get('[data-testid="local-users-notice"]').text()).toContain('已就绪');
  });

  it('refuses to render the surface for a non-admin', async () => {
    signIn(['release_admin']);

    const wrapper = await mountPage();

    expect(wrapper.text()).toContain('platform_admin');
    expect(wrapper.find('form').exists()).toBe(false);
    expect(mockedList).not.toHaveBeenCalled();
  });

  // bcrypt caps at 72 bytes: the form must say so instead of sending a request that can
  // only come back as an opaque INTERNAL.
  it('refuses an oversized password before sending it', async () => {
    signIn(['platform_admin']);
    mockedList.mockResolvedValue({ users: [], nextCursor: '' });

    const wrapper = await mountPage();
    await wrapper.get('input[name="username"]').setValue('erin');
    await wrapper.get('input[name="password"]').setValue('x'.repeat(80));
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.text()).toContain('密码过长');
    expect(mockedCreate).not.toHaveBeenCalled();
  });

  // 25 CJK characters are 75 bytes: the limit is bytes, not characters, and a
  // character-count implementation would let this through.
  it('refuses a multi-byte password that exceeds the byte limit', async () => {
    signIn(['platform_admin']);
    mockedList.mockResolvedValue({ users: [], nextCursor: '' });

    const wrapper = await mountPage();
    await wrapper.get('input[name="username"]').setValue('frank');
    await wrapper.get('input[name="password"]').setValue('密'.repeat(25));
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.text()).toContain('密码过长');
    expect(mockedCreate).not.toHaveBeenCalled();
  });

  it('shows the server refusal when a create fails', async () => {
    signIn(['platform_admin']);
    mockedList.mockResolvedValue({ users: [], nextCursor: '' });
    mockedCreate.mockRejectedValue(new Error('boom'));

    const wrapper = await mountPage();
    await wrapper.get('input[name="username"]').setValue('dave');
    await wrapper.get('input[name="password"]').setValue('pw');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    // The failure surface must carry the mapped copy, not merely some non-empty text.
    expect(wrapper.get('[role="alert"]').text()).toContain('不可用');
  });
});
