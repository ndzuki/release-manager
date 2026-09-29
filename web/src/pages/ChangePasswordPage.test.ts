import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import ChangePasswordPage from './ChangePasswordPage.vue';
import * as api from '@/connect/auth-api';
import { useAuthStore } from '@/stores/auth';

vi.mock('@/connect/auth-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, changePassword: vi.fn() };
});

const mockedChange = vi.mocked(api.changePassword);

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/settings/password', name: 'ChangePassword', component: ChangePasswordPage },
      { path: '/login', name: 'Login', component: { template: '<div />' } },
      { path: '/', name: 'Home', component: { template: '<div />' } },
    ],
  });
  await router.push('/settings/password');
  await router.isReady();
  const wrapper = mount(ChangePasswordPage, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

async function fill(wrapper: Awaited<ReturnType<typeof mountPage>>['wrapper'], oldPw: string, newPw: string, confirm: string) {
  await wrapper.get('input[name="oldPassword"]').setValue(oldPw);
  await wrapper.get('input[name="newPassword"]').setValue(newPw);
  await wrapper.get('input[name="confirmPassword"]').setValue(confirm);
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedChange.mockReset();
});

describe('ChangePasswordPage', () => {
  it('does not greet an untouched form with a validation error', async () => {
    const { wrapper } = await mountPage();
    expect(wrapper.find('[data-testid="change-password-validation"]').exists()).toBe(false);
  });

  it('blocks submit until every field is filled and the confirmation matches', async () => {
    const { wrapper } = await mountPage();

    await wrapper.get('form').trigger('submit');
    expect(wrapper.get('[data-testid="change-password-validation"]').text()).toContain('请填写全部三项');
    expect(mockedChange).not.toHaveBeenCalled();

    await fill(wrapper, 'old-secret', 'new-secret', 'other-secret');
    await wrapper.get('form').trigger('submit');
    expect(wrapper.get('[data-testid="change-password-validation"]').text()).toContain('两次输入的新密码不一致');
    expect(mockedChange).not.toHaveBeenCalled();

    await fill(wrapper, 'same-secret', 'same-secret', 'same-secret');
    await wrapper.get('form').trigger('submit');
    expect(wrapper.get('[data-testid="change-password-validation"]').text()).toContain('不能与当前密码相同');
    expect(mockedChange).not.toHaveBeenCalled();
  });

  it('refuses a new password above the bcrypt byte limit before calling the RPC', async () => {
    const { wrapper } = await mountPage();

    // 73 bytes: bcrypt's limit, surfaced by the server only as an opaque INTERNAL.
    await fill(wrapper, 'old-secret', 'ü'.repeat(37), 'ü'.repeat(37));
    await wrapper.get('form').trigger('submit');

    expect(wrapper.get('[data-testid="change-password-validation"]').text()).toContain('上限 72 字节');
    expect(mockedChange).not.toHaveBeenCalled();
  });

  it('reports a wrong old password without touching the session', async () => {
    // REQ-025 error model: UNAUTHENTICATED with `invalid old password`.
    mockedChange.mockRejectedValue(new ConnectError('invalid old password', Code.Unauthenticated));
    const { wrapper } = await mountPage();
    const auth = useAuthStore();
    const clearSpy = vi.spyOn(auth, 'clearSession');

    await fill(wrapper, 'wrong', 'new-secret', 'new-secret');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.text()).toContain('旧密码不正确');
    expect(clearSpy).not.toHaveBeenCalled();
    expect(wrapper.find('[data-testid="change-password-success"]').exists()).toBe(false);
  });

  it('distinguishes an expired session from a wrong old password', async () => {
    mockedChange.mockRejectedValue(new ConnectError('authentication required', Code.Unauthenticated));
    const { wrapper } = await mountPage();

    await fill(wrapper, 'old-secret', 'new-secret', 'new-secret');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.text()).toContain('会话已失效');
    expect(wrapper.text()).not.toContain('旧密码不正确');
  });

  it('clears the session and returns to Login after a successful change (AC-025-03)', async () => {
    vi.useFakeTimers();
    mockedChange.mockResolvedValue(undefined);
    const { wrapper, router } = await mountPage();
    const auth = useAuthStore();
    const clearSpy = vi.spyOn(auth, 'clearSession');

    await fill(wrapper, 'old-secret', 'new-secret', 'new-secret');
    await wrapper.get('form').trigger('submit');
    await vi.advanceTimersByTimeAsync(0);

    // The server revokes every session, so the console must drop it locally.
    expect(clearSpy).toHaveBeenCalledWith('anonymous');
    expect(wrapper.find('[data-testid="change-password-success"]').exists()).toBe(true);

    await vi.advanceTimersByTimeAsync(1300);
    await flushPromises();
    expect(router.currentRoute.value.name).toBe('Login');
    vi.useRealTimers();
  });

  it('sends both passwords to the RPC', async () => {
    mockedChange.mockResolvedValue(undefined);
    const { wrapper } = await mountPage();

    await fill(wrapper, 'old-secret', 'new-secret', 'new-secret');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(mockedChange).toHaveBeenCalledWith('old-secret', 'new-secret');
  });
});
