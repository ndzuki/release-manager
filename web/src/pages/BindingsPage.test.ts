import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import BindingsPage from './BindingsPage.vue';
import { useAuthStore } from '@/stores/auth';
import { useGovernanceStore } from '@/stores/governance';
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

// One ListBindings row per state, so a test can hand back the post-revoke list and
// prove the page rendered the refresh instead of the stale rows.
const CUSTOMER_A_ACTIVE = {
  id: 'b1',
  orgId: 'org-1',
  customerId: 'customer-a',
  status: 'active' as const,
  optimisticVersion: 2n,
  createdAt: '2026-09-28T00:00:00Z',
  updatedAt: null,
};
const CUSTOMER_A_REVOKED = { ...CUSTOMER_A_ACTIVE, status: 'revoked' as const };
const CUSTOMER_B_REVOKED = {
  id: 'b2',
  orgId: 'org-1',
  customerId: 'customer-b',
  status: 'revoked' as const,
  optimisticVersion: 4n,
  createdAt: null,
  updatedAt: null,
};

const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  // Unmount before wiping the DOM: the focus trap keeps a module-level stack of open
  // overlays, and a component that is never unmounted leaves a stale entry behind
  // (and a teleported dialog that the next test's getByRole would then count twice).
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

function signIn(): void {
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'me', roles: ['platform_admin'], activeOrgId: 'org-1' },
    organizations: [{ $typeName: 'auth.v1.Organization', id: 'org-1', name: 'Platform', status: 'active' }],
  });
}

async function mountPage(options: { attachTo?: boolean } = {}) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/settings/bindings', name: 'GovernanceBindings', component: BindingsPage }],
  });
  await router.push('/settings/bindings');
  await router.isReady();
  // attachTo body (not the default detached div) where the test asserts
  // document.activeElement: focus only resolves for attached elements (the same
  // reason TrustPolicyPage.test.ts attaches).
  const wrapper = options.attachTo
    ? mount(BindingsPage, { attachTo: document.body, global: { plugins: [router] } })
    : mount(BindingsPage, { global: { plugins: [router] } });
  mounted.push(wrapper);
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
  mockedList.mockReset().mockResolvedValue([CUSTOMER_A_ACTIVE, CUSTOMER_B_REVOKED]);
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
   * flipped to 已撤销 with no dialog). The cases below pin the confirm step:
   * arm -> confirm -> cancel/Escape, with revokeBinding as the observable RPC.
   *
   * Trigger caveat: VTU v2.4.11's trigger('click') silently skips disabled
   * elements, so "revokeBinding was not called" proves nothing on its own. The
   * enabled assertion plus the real modal contract (TASK-297: role, aria-modal and
   * the accessible name resolved through aria-labelledby — it can only appear if
   * the click handler actually ran) are what make the negative valid.
   */
  it('does not revoke on the row click — it arms the confirmation instead', async () => {
    const wrapper = await mountPage();

    const revokeButton = wrapper.get('[data-testid="binding-row-customer-a"] button');
    expect((revokeButton.element as HTMLButtonElement).disabled).toBe(false);
    await revokeButton.trigger('click');

    expect(mockedRevoke).not.toHaveBeenCalled();
    // TASK-297 ①: `getByRole('alertdialog')).toBeTruthy()` was vacuous (the helper
    // throws when the element is missing). Pin the properties that make it the
    // modal the user is actually trapped in.
    const dialog = getByRole('alertdialog');
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    const titleId = dialog.getAttribute('aria-labelledby');
    expect(titleId).toBeTruthy();
    expect(document.getElementById(titleId ?? '')?.textContent).toBe('撤销绑定');
    expect(dialog.getAttribute('tabindex')).toBe('-1');
  });

  it('revokes once, with the binding id and version, only after confirming', async () => {
    // The page goes through the store action, not the RPC directly.
    const revokeAction = vi.spyOn(useGovernanceStore(), 'revoke');
    const wrapper = await mountPage({ attachTo: true });
    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');

    const dialog = getByRole('alertdialog');
    // TASK-297 ③: the description names the exact customer and the exact
    // consequence; `toContain('能力')` passed even with the wrong customer id.
    expect(dialog.querySelector('.app-dialog__description')?.textContent).toBe(
      '确认后将立即撤销客户 customer-a 的绑定，并收回该绑定授予的能力，此操作不可恢复。',
    );

    // Revocation mutates a live row: what the page shows next comes from the
    // ListBindings refresh, so hand back the post-revoke list (TASK-297 ②).
    mockedList.mockResolvedValue([CUSTOMER_A_REVOKED, CUSTOMER_B_REVOKED]);
    clickButton(dialog, '确认撤销');
    await flushPromises();

    expect(revokeAction).toHaveBeenCalledWith(expect.objectContaining({ id: 'b1', customerId: 'customer-a' }));
    expect(mockedRevoke).toHaveBeenCalledTimes(1);
    expect(mockedRevoke).toHaveBeenCalledWith('b1', 2n);
    expect(mockedList).toHaveBeenCalledTimes(2);
    expect(wrapper.get('[data-testid="binding-row-customer-a"]').text()).toContain('已撤销');
    expect(wrapper.find('[data-testid="binding-row-customer-a"] button').exists()).toBe(false);
    // Success closes the dialog, and focus lands on the outcome notice: the row
    // button it used to sit on is gone, so it must not fall back to <body>
    // (TASK-297 #2).
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
    expect(document.activeElement).toBe(wrapper.get('[data-testid="governance-notice"]').element);
  });

  /*
   * TASK-297 #1: keeping the dialog open is only useful if the reason is in it.
   * The page-level <ErrorState> renders behind AppDialog's fixed backdrop, so the
   * failure copy has to be a descendant of the panel as well.
   */
  it('keeps the failure reason inside the still-open dialog', async () => {
    mockedRevoke.mockRejectedValue(new ConnectError('revoke failed', Code.Internal));
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');

    const dialog = getByRole('alertdialog');
    clickButton(dialog, '确认撤销');
    await flushPromises();

    // TASK-295's intent: a failed revoke leaves the confirmation armed.
    expect(document.querySelector('[role="alertdialog"]')).toBe(dialog);
    // TASK-303: `const failure = dialog.querySelector(...)` + `dialog.contains(failure)`
    // could never fail — `contains` of a node taken FROM the dialog is always true,
    // and `failure === null` was already covered by the line above. Pin the count
    // instead: exactly one copy of the reason, and it lives inside the panel.
    const insideDialog = dialog.querySelectorAll<HTMLElement>('[data-testid="revoke-failure"]');
    expect(insideDialog).toHaveLength(1);
    expect(insideDialog[0]?.textContent?.trim()).toBe('操作失败，请稍后重试');
    expect(document.querySelectorAll('[data-testid="revoke-failure"]')).toHaveLength(1);
    // The arm survived the failure: the row still offers its confirm.
    expect(wrapper.find('[data-testid="binding-row-customer-a"] button').exists()).toBe(true);
  });

  /*
   * TASK-303 ①: the panel used to render the page-level `store.failure`, which
   * every operation writes. A user who merely ARMED the confirmation therefore saw
   * — and `role="alert"` announced — the previous operation's error (here the grant
   * refusal), even though no revoke had been attempted.
   */
  it("does not show another operation's failure inside the revoke dialog", async () => {
    mockedGrant.mockRejectedValue(new ConnectError('permission denied', Code.PermissionDenied));
    const wrapper = await mountPage();

    // An unrelated operation fails first and lands in the page-level store.
    await wrapper.get('input[name="subject"]').setValue('user-9');
    await wrapper.get('form.governance__panel:last-of-type').trigger('submit');
    await flushPromises();
    expect(wrapper.get('.error-state').text()).toContain('组织管理员');

    // Arming the confirmation must not inherit that copy.
    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');
    await flushPromises();

    // TASK-303 review: the negative assertions below must be backed by the dialog actually being
    // open, otherwise a no-op arm (the dialog never opens) would satisfy them.
    expect(document.querySelector('[role="alertdialog"]')).not.toBeNull();

    expect(mockedRevoke).not.toHaveBeenCalled();
    expect(document.querySelector('[data-testid="revoke-failure"]')).toBeNull();
  });

  // TASK-297 ④: cancel and Escape shared one `it`, so a broken first branch hid a
  // broken second one. They are two independent zero-side-effect exits.
  it('cancel dismisses the confirmation with no revoke', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');

    clickButton(getByRole('alertdialog'), '取消');
    await flushPromises();

    expect(mockedRevoke).not.toHaveBeenCalled();
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
  });

  it('Escape dismisses the confirmation with no revoke', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');

    // AppDialog handles Escape at the document level, so the trap entry needs the
    // render tick first.
    await flushPromises();
    // TASK-303: `expect(getByRole('alertdialog')).toBeTruthy()` could never fail —
    // the helper throws when the element is missing, so the assertion never saw a
    // false value. Pin the properties that make it the modal being dismissed.
    const dialog = getByRole('alertdialog');
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(document.getElementById(dialog.getAttribute('aria-labelledby') ?? '')?.textContent).toBe('撤销绑定');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();

    expect(mockedRevoke).not.toHaveBeenCalled();
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
  });
});
