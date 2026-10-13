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
const mockedRevoke = vi.mocked(api.revokeBinding);

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

/*
 * AppDialog teleports to <body> (ADR-029), so a test wrapper's scoped queries
 * cannot see it; the repo's dialog tests query the document instead
 * (OperatorPages.test.ts, AppDialog.test.ts). @testing-library is not a
 * dependency here, so this mirrors getByRole's contract — the unique element
 * carrying that role, and a throw when there is none (or more than one).
 */
function getByRole(role: string): HTMLElement {
  const matches = Array.from(document.querySelectorAll<HTMLElement>(`[role="${role}"]`));
  if (matches.length !== 1) throw new Error(`expected exactly one role="${role}", found ${matches.length}`);
  return matches[0]!;
}

function clickButton(root: HTMLElement, label: string): void {
  const button = Array.from(root.querySelectorAll('button')).find((element) => element.textContent?.trim() === label);
  if (!button) throw new Error(`dialog button not found: ${label}`);
  button.click();
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue([
    { id: 'b1', orgId: 'org-1', customerId: 'customer-a', status: 'active', optimisticVersion: 2n, createdAt: '2026-09-28T00:00:00Z', updatedAt: null },
    { id: 'b2', orgId: 'org-1', customerId: 'customer-b', status: 'revoked', optimisticVersion: 4n, createdAt: null, updatedAt: null },
  ]);
  mockedCreate.mockReset().mockResolvedValue(undefined);
  mockedGrant.mockReset().mockResolvedValue({ sourceVersion: 1n, policyVersion: 2n });
  mockedRevoke.mockReset().mockResolvedValue(undefined);
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

  /*
   * TASK-295: the row button used to call store.revoke directly, so one misclick
   * withdrew the binding and its capability grant (TASK-293 measured it: the row
   * flipped to 已撤销 with no dialog). The three cases below pin the confirm step:
   * arm -> confirm -> cancel/Escape, with revokeBinding as the observable RPC.
   *
   * Trigger caveat: VTU v2.4.11's trigger('click') silently skips disabled
   * elements, so "revokeBinding was not called" proves nothing on its own. The
   * enabled assertion plus the getByRole('alertdialog') match (which can only
   * appear if the click handler actually ran) are what make the negative valid.
   */
  it('does not revoke on the row click — it arms the confirmation instead', async () => {
    const wrapper = await mountPage();

    const revokeButton = wrapper.get('[data-testid="binding-row-customer-a"] button');
    expect((revokeButton.element as HTMLButtonElement).disabled).toBe(false);
    await revokeButton.trigger('click');

    expect(mockedRevoke).not.toHaveBeenCalled();
    expect(getByRole('alertdialog')).toBeTruthy();
    wrapper.unmount();
  });

  it('revokes once, with the binding id and version, only after confirming', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');

    const dialog = getByRole('alertdialog');
    expect(dialog.textContent).toContain('能力');

    clickButton(dialog, '确认撤销');
    await flushPromises();

    expect(mockedRevoke).toHaveBeenCalledTimes(1);
    expect(mockedRevoke).toHaveBeenCalledWith('b1', 2n);
    // Success closes it; the store refreshed the list (notice is the store's).
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
    expect(wrapper.get('[data-testid="governance-notice"]').text()).toContain('customer-a');
    wrapper.unmount();
  });

  it('cancel and Escape dismiss the confirmation with no revoke', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');

    clickButton(getByRole('alertdialog'), '取消');
    await flushPromises();

    expect(mockedRevoke).not.toHaveBeenCalled();
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();

    // Escape is the other zero-side-effect exit; AppDialog handles it at the
    // document level, so the trap entry needs the render tick first.
    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');
    await flushPromises();
    expect(getByRole('alertdialog')).toBeTruthy();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();

    expect(mockedRevoke).not.toHaveBeenCalled();
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
    wrapper.unmount();
  });
});
