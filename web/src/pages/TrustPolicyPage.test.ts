import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
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
    createTrustRoot: vi.fn(),
    rotateTrustRoot: vi.fn(),
    endGrace: vi.fn(),
    retireTrustRoot: vi.fn(),
    revokeTrustRoot: vi.fn(),
  };
});

const mockedPolicy = vi.mocked(api.getTrustPolicy);
const mockedCreate = vi.mocked(api.createTrustRoot);
const mockedRotate = vi.mocked(api.rotateTrustRoot);
const mockedRevoke = vi.mocked(api.revokeTrustRoot);

const PUBLIC_PEM = '-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEAGb9ECWmEzf6FQbrBZ9w7lshQhqowtrbLDFw4rXAxZuE=\n-----END PUBLIC KEY-----';

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
  mockedCreate.mockReset().mockResolvedValue(root('key-3', 'active'));
  mockedRotate.mockReset().mockResolvedValue({ oldRoot: root('key-new', 'grace'), newRoot: root('key-3', 'active') });
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

  /*
   * TASK-269. The environment selector sat in the middle of a sentence ("环境 <select>")
   * with no label at all: the prose next to a control is not an accessible name. The
   * word is now the control's <label>, and the assertion is that the reference resolves
   * to the real selector — not merely that a `for` attribute exists.
   */
  it('associates the environment selector with its label', async () => {
    const wrapper = await mountPage();

    const label = wrapper.get('label[for="trust-environment"]');
    const select = wrapper.get<HTMLSelectElement>('#trust-environment');

    expect(label.text()).toBe('环境');
    expect(select.element.tagName).toBe('SELECT');
    expect(select.attributes('name')).toBe('environment');
    // …and it is the selector the policy was loaded for, not a look-alike.
    expect(select.element.value).toBe('staging');
    expect([...select.element.options].map((option) => option.value)).toEqual(['staging', 'production']);
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
    expect(wrapper.find('[data-testid="trust-create-entry"]').exists()).toBe(false);
  });
});

/*
 * TASK-280 D11. Create and rotate were the only two TrustService RPCs with no console
 * entry point. The material contract (api/proto/trust/v1/trust.proto:78,94;
 * internal/trust/types.go:112-114) is PUBLIC key only, so the form submits a pasted
 * public PEM and refuses anything marked PRIVATE before the RPC is reached.
 */
describe('trust-root create and rotate', () => {
  async function fillAndSubmit(wrapper: Awaited<ReturnType<typeof mountPage>>, keyId = 'key-3', issuer = 'CN=next'): Promise<void> {
    await wrapper.get('[data-testid="trust-editor-key-id"]').setValue(keyId);
    await wrapper.get('[data-testid="trust-editor-issuer"]').setValue(issuer);
    await wrapper.get('[data-testid="trust-editor-public-key"]').setValue(PUBLIC_PEM);
    await wrapper.get('form.trust-editor').trigger('submit');
    await flushPromises();
  }

  it('creates a root with the environment and operator from the session', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-create-entry"]').trigger('click');
    await fillAndSubmit(wrapper);

    expect(mockedCreate).toHaveBeenCalledWith({
      environment: 'staging',
      operator: 'dev-admin',
      keyId: 'key-3',
      issuer: 'CN=next',
      subjectPattern: '',
      publicKeyPem: PUBLIC_PEM,
    });
    // Success closes the panel; the page reports it through the notice line.
    expect(wrapper.find('[data-testid="trust-editor-submit"]').exists()).toBe(false);
    expect(wrapper.get('[data-testid="trust-notice"]').text()).toContain('key-3');
  });

  it('rotates the clicked root id and carries a grace window for the old key', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-rotate-key-new"]').trigger('click');
    expect(wrapper.text()).toContain('轮换信任根 key-new');
    await fillAndSubmit(wrapper);

    const sent = mockedRotate.mock.calls[0]![0];
    expect(sent.oldRootId).toBe('root-key-new');
    expect(sent.environment).toBe('staging');
    expect(sent.operator).toBe('dev-admin');
    expect(sent.publicKeyPem).toBe(PUBLIC_PEM);
    expect(sent.graceUntil).toBeInstanceOf(Date);
    expect(sent.graceUntil!.getTime()).toBeGreaterThan(Date.now());
  });

  it('never sends a private key and names the mistake', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-create-entry"]').trigger('click');
    await wrapper.get('[data-testid="trust-editor-key-id"]').setValue('key-3');
    await wrapper.get('[data-testid="trust-editor-issuer"]').setValue('CN=next');
    await wrapper.get('[data-testid="trust-editor-public-key"]').setValue('-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----');
    await wrapper.get('form.trust-editor').trigger('submit');
    await flushPromises();

    expect(mockedCreate).not.toHaveBeenCalled();
    expect(wrapper.get('.form-field__error').text()).toContain('私钥');
  });

  it('reports a permission refusal to the operator', async () => {
    mockedCreate.mockRejectedValue(new ConnectError('denied', Code.PermissionDenied));
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="trust-create-entry"]').trigger('click');
    await fillAndSubmit(wrapper);

    expect(wrapper.text()).toContain('无权修改信任根');
  });
});
