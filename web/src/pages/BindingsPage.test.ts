import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import BindingsPage from './BindingsPage.vue';
import { useAuthStore } from '@/stores/auth';
import * as api from '@/connect/binding-api';

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
const mockedGrant = vi.mocked(api.setCapabilityGrant);

function signIn(): void {
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'me', roles: ['platform_admin'], activeOrgId: 'org-1' },
    organizations: [{ $typeName: 'auth.v1.Organization', id: 'org-1', name: 'Platform', status: 'active' }],
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
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue([
    { id: 'b1', orgId: 'org-1', customerId: 'customer-a', status: 'active', optimisticVersion: 2n, createdAt: '2026-09-28T00:00:00Z', updatedAt: null },
    { id: 'b2', orgId: 'org-1', customerId: 'customer-b', status: 'revoked', optimisticVersion: 4n, createdAt: null, updatedAt: null },
  ]);
  mockedCreate.mockReset().mockResolvedValue(undefined);
  mockedGrant.mockReset().mockResolvedValue({ sourceVersion: 1n, policyVersion: 2n });
  signIn();
});

describe('BindingsPage', () => {
  it('lists active and revoked bindings with their status', async () => {
    const wrapper = await mountPage();

    expect(mockedList).toHaveBeenCalledWith('org-1');
    expect(wrapper.get('[data-testid="binding-row-customer-a"]').text()).toContain('生效中');
    expect(wrapper.get('[data-testid="binding-row-customer-b"]').text()).toContain('已撤销');
  });

  it('offers revoke only for active bindings', async () => {
    const wrapper = await mountPage();

    expect(wrapper.find('[data-testid="binding-row-customer-a"] button').exists()).toBe(true);
    expect(wrapper.find('[data-testid="binding-row-customer-b"] button').exists()).toBe(false);
  });

  it('requires a customer id before binding', async () => {
    const wrapper = await mountPage();

    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('[data-testid="binding-create-error"]').text()).toContain('请填写客户 ID');
    expect(mockedCreate).not.toHaveBeenCalled();
  });

  // The server refuses an active duplicate with ALREADY_EXISTS; the page explains
  // the same thing without spending a request.
  it('refuses an already active customer locally', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="customerId"]').setValue('customer-a');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.get('[data-testid="binding-create-error"]').text()).toContain('已绑定');
    expect(mockedCreate).not.toHaveBeenCalled();
  });

  it('binds a new customer and reports success', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="customerId"]').setValue('customer-c');
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(mockedCreate).toHaveBeenCalledWith('org-1', 'customer-c');
    expect(wrapper.get('[data-testid="governance-notice"]').text()).toContain('customer-c');
  });

  it('applies a capability grant and shows the returned versions', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="subject"]').setValue('user-9');
    await wrapper.get('select[name="action"]').setValue('release.emergency.resolve');
    await wrapper.get('select[name="desiredState"]').setValue('grant');
    await wrapper.get('form.governance__panel:last-of-type').trigger('submit');
    await flushPromises();

    expect(mockedGrant).toHaveBeenCalledWith('org-1', 'user-9', 'release.emergency.resolve', false);
    expect(wrapper.get('[data-testid="grant-versions"]').text()).toContain('来源 / 策略 1');
  });

  it('requires a subject before applying a capability', async () => {
    const wrapper = await mountPage();

    await wrapper.get('form.governance__panel:last-of-type').trigger('submit');
    await flushPromises();

    expect(wrapper.get('[data-testid="grant-error"]').text()).toContain('请填写主体');
    expect(mockedGrant).not.toHaveBeenCalled();
  });

  // A REAL PermissionDenied never reaches this component: the session interceptor in
  // client.ts hands it to the auth error handler, which navigates to /forbidden
  // (verified in the dev environment). What this case pins is only that the page
  // renders whatever failure the store exposes — the refusal the user actually sees is
  // the Forbidden page, and that policy is covered in client.test.ts.
  it('renders the failure the store exposes (the real 403 navigates to /forbidden)', async () => {
    mockedGrant.mockRejectedValue(new ConnectError('permission denied', Code.PermissionDenied));
    const wrapper = await mountPage();

    await wrapper.get('input[name="subject"]').setValue('user-9');
    await wrapper.get('form.governance__panel:last-of-type').trigger('submit');
    await flushPromises();

    expect(wrapper.get('.error-state').text()).toContain('组织管理员');
  });
});
