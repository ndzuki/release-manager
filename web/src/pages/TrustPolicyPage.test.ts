import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import TrustPolicyPage from './TrustPolicyPage.vue';
import { useAuthStore } from '@/stores/auth';
import * as api from '@/connect/trust-api';

vi.mock('@/connect/trust-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return {
    ...original,
    getTrustPolicy: vi.fn(),
    endGrace: vi.fn(),
    retireTrustRoot: vi.fn(),
    revokeTrustRoot: vi.fn(),
  };
});

const mockedPolicy = vi.mocked(api.getTrustPolicy);
const mockedRevoke = vi.mocked(api.revokeTrustRoot);

function root(keyId: string, state: api.TrustRootStateName): api.TrustRootView {
  return {
    id: `root-${keyId}`,
    keyId,
    issuer: 'CN=dev',
    subjectPattern: 'release-manager',
    publicKeyPem: 'pem',
    state,
    validFrom: '2026-09-28T00:00:00Z',
    graceUntil: state === 'grace' ? '2026-10-05T00:00:00Z' : null,
    createdAt: null,
    updatedAt: null,
    revokedAt: null,
  };
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/settings/trust', name: 'TrustPolicy', component: TrustPolicyPage }],
  });
  await router.push('/settings/trust');
  await router.isReady();
  const wrapper = mount(TrustPolicyPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
  useAuthStore().$patch({ status: 'authenticated', initialized: true, user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-admin', roles: ['platform_admin'], activeOrgId: 'org-1' } });
  mockedPolicy.mockReset().mockResolvedValue({
    environment: 'staging',
    version: 5n,
    revocationEpoch: 2n,
    roots: [root('key-new', 'active'), root('key-old', 'grace'), root('key-gone', 'revoked')],
  });
  mockedRevoke.mockReset().mockResolvedValue(undefined);
});

describe('TrustPolicyPage', () => {
  it('shows the policy header, every root and the live count', async () => {
    const wrapper = await mountPage();

    expect(mockedPolicy).toHaveBeenCalledWith('staging');
    const text = wrapper.text();
    expect(text).toContain('策略版本');
    expect(text).toContain('5');
    expect(text).toContain('吊销代数');
    expect(wrapper.find('[data-testid="trust-root-key-new"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="trust-root-key-old"]').text()).toContain('宽限期');
    expect(wrapper.find('[data-testid="trust-root-key-gone"]').text()).toContain('已吊销');
  });

  // The action set is the state machine: active can retire/revoke, grace adds
  // end_grace, retired/revoked offer nothing.
  it('offers only the actions the root state accepts', async () => {
    const wrapper = await mountPage();

    expect(wrapper.find('[data-testid="trust-retire-key-new"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="trust-revoke-key-new"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="trust-end_grace-key-new"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="trust-end_grace-key-old"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="trust-retire-key-gone"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="trust-revoke-key-gone"]').exists()).toBe(false);
  });

  it('treats an environment without roots as an informational empty state', async () => {
    mockedPolicy.mockResolvedValue({ environment: 'production', version: 1n, revocationEpoch: 0n, roots: [] });
    const wrapper = await mountPage();

    expect(wrapper.text()).toContain('还没有信任根');
    expect(wrapper.find('.error-state').exists()).toBe(false);
  });

  it('requires confirmation before revoking, and revokes with the row id', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-revoke-key-new"]').trigger('click');
    expect(mockedRevoke).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain('确认吊销信任根 key-new');

    await wrapper.get('[data-testid="trust-confirm-revoke"]').trigger('click');
    await flushPromises();

    expect(mockedRevoke).toHaveBeenCalledWith('staging', 'root-key-new', 'dev-admin');
  });

  it('cancels the revoke confirmation without calling the RPC', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-revoke-key-new"]').trigger('click');
    await wrapper.findAll('button').filter((b) => b.text() === '取消')[0]!.trigger('click');
    await flushPromises();

    expect(mockedRevoke).not.toHaveBeenCalled();
    expect(wrapper.text()).not.toContain('确认吊销信任根');
  });

  // Refusing to remove the last live root is a documented server rule; the row must
  // not even offer the action.
  it('disables removal when the environment has a single live root', async () => {
    mockedPolicy.mockResolvedValue({
      environment: 'staging',
      version: 2n,
      revocationEpoch: 0n,
      roots: [root('only', 'active'), root('old', 'retired')],
    });
    const wrapper = await mountPage();

    const retire = wrapper.get<HTMLButtonElement>('[data-testid="trust-retire-only"]');
    expect(retire.element.disabled).toBe(true);
  });
});

describe('trust-root write gating', () => {
  it('hides the rotate/retire/revoke buttons from a non-admin', async () => {
    useAuthStore().$patch({
      status: 'authenticated',
      initialized: true,
      user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-deployer', roles: ['deployer'], activeOrgId: 'org-1' },
    });

    const wrapper = await mountPage();

    // The server marks those procedures adminOnly, so a deployer would get a 403.
    expect(wrapper.findAll('[data-testid^="trust-rotate-"]').length).toBe(0);
    expect(wrapper.findAll('[data-testid^="trust-revoke-"]').length).toBe(0);
  });
});
