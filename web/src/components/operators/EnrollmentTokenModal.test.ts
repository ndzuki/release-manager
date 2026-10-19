import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import EnrollmentTokenModal from './EnrollmentTokenModal.vue';
import { useOperatorStore } from '@/stores/operator';
import { createEnrollmentToken, revokePendingEnrollmentToken } from '@/connect/operator-api';
import { t, type MessageKey } from '@/i18n/messages';
import type * as OperatorApi from '@/connect/operator-api';

vi.mock('@/connect/operator-api', async (importOriginal) => {
  const original = await importOriginal<typeof OperatorApi>();
  return {
    ...original,
    createEnrollmentToken: vi.fn(),
    revokePendingEnrollmentToken: vi.fn(),
  };
});

const GATEWAY = 'https://10.10.33.122:8444';
const result = {
  token: 'plaintext-token',
  expiresAt: '2026-07-27T13:00:00.000Z',
  customerId: 'customer-1',
  clusterId: 'cluster-1',
  clusterName: 'Staging',
  operatorEndpoint: GATEWAY,
  installCommandTemplateVersion: 'v2',
  // The shape of the server template (internal/orchestrator/operator.go): the gateway
  // placeholder appears both in the quoted argument and in the instruction comment that
  // tells the reader to replace it. The console has to handle both to make the copy
  // executable (TASK-389).
  installCommandTemplate: [
    '# Replace <CONTROL_PLANE_OPERATOR_GATEWAY> with the address this customer cluster',
    '# can reach (TLS; e.g. an ingress or nodePort in front of the operator Service).',
    'export ENROLLMENT_TOKEN=${ENROLLMENT_TOKEN}',
    "release-operator --config operator.yaml --orchestrator-addr '<CONTROL_PLANE_OPERATOR_GATEWAY>'",
  ].join('\n'),
};

const GATEWAY_PLACEHOLDER = '<CONTROL_PLANE_OPERATOR_GATEWAY>';

/*
 * TASK-389 review block: `operator_endpoint` is still a control-plane fixture address
 * (`cmd/orchestrator/main.go`), so a non-empty value is NOT proof that the command can
 * connect. These are the shapes the server can advertise today; each has to take the
 * same fail-closed branch as an empty endpoint. `operator.example.com` is deliberately
 * absent: a public host that merely starts with the Service name is reachable.
 */
const UNREACHABLE_GATEWAYS: ReadonlyArray<{ endpoint: string; reason: MessageKey }> = [
  {
    endpoint: 'https://operator-gateway.dev.release-manager.local:8084',
    reason: 'operator.token.gatewayReasonDevLocal',
  },
  // The scheme is checked first: the gateway-disabled fallback is plaintext, so it
  // reports the TLS violation before the cluster-internal host name.
  { endpoint: 'http://operator:8084', reason: 'operator.token.gatewayReasonPlaintext' },
  { endpoint: 'https://operator:8444', reason: 'operator.token.gatewayReasonClusterDns' },
  {
    endpoint: 'https://operator.default.svc.cluster.local:8084',
    reason: 'operator.token.gatewayReasonClusterDns',
  },
  { endpoint: 'http://10.10.33.122:8444', reason: 'operator.token.gatewayReasonPlaintext' },
  { endpoint: 'not a url', reason: 'operator.token.gatewayReasonInvalid' },
];

function createStorage(): Storage {
  const values = new Map<string, string>();
  return {
    get length() { return values.size; },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => values.delete(key),
    setItem: (key, value) => values.set(key, value),
  };
}

Object.defineProperty(window, 'localStorage', { configurable: true, value: createStorage() });
Object.defineProperty(window, 'sessionStorage', { configurable: true, value: createStorage() });

beforeEach(() => {
  vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
  setActivePinia(createPinia());
  window.localStorage.clear();
  window.sessionStorage.clear();
  useOperatorStore().enrollmentForm = { operatorName: 'operator-one', ttlMinutes: 5 };
  window.history.replaceState({}, '', '/customers/customer-1/clusters/cluster-1/operators/new');
  vi.mocked(createEnrollmentToken).mockReset().mockResolvedValue(result);
  vi.mocked(revokePendingEnrollmentToken).mockReset().mockResolvedValue(true);
});

describe('EnrollmentTokenModal', () => {
  async function mountModal() {
    const wrapper = mount(EnrollmentTokenModal, {
      global: { stubs: { Teleport: true } },
      props: { customerId: 'customer-1', clusterId: 'cluster-1' },
    });
    await flushPromises();
    return wrapper;
  }

  async function mountWithEndpoint(endpoint: string) {
    vi.mocked(createEnrollmentToken).mockResolvedValueOnce({ ...result, operatorEndpoint: endpoint });
    return mountModal();
  }

  /*
   * The fail-closed contract for an endpoint the customer cluster cannot dial: the
   * placeholder stays quoted, the token substitution still happens, the warning names
   * the reason, and the command carries one remediation line right where the raw
   * template's English instruction block used to be. A command that "looks ready" is
   * the failure this pins against.
   */
  function expectGatewayFailsClosed(wrapper: VueWrapper, endpoint: string, reasonKey: MessageKey): void {
    const command = wrapper.find('pre').text();
    expect(command).toContain(`--orchestrator-addr '${GATEWAY_PLACEHOLDER}'`);
    expect(command).not.toContain(endpoint);
    expect(command).toContain('plaintext-token');
    expect(command).not.toContain('${ENROLLMENT_TOKEN}');
    expect(command).toContain(`# ${t('operator.token.gatewayRemediation')}`);

    // The advertised value stays visible for diagnosis; the alert explains why it is
    // not armed into the command.
    expect(wrapper.find('[data-testid="enrollment-gateway"]').text()).toBe(endpoint);
    const notice = wrapper.find('[data-testid="enrollment-gateway-missing"]');
    expect(notice.exists()).toBe(true);
    expect(notice.attributes('role')).toBe('alert');
    expect(notice.text()).toBe(t('operator.token.gatewayUnreachableWarning', { reason: t(reasonKey) }));
    expect(notice.text()).toContain(GATEWAY_PLACEHOLDER);
  }

  it('keeps plaintext local to the modal and clears it after confirmed close', async () => {
    const wrapper = mount(EnrollmentTokenModal, {
      // The overlay is teleported by AppDialog; this test is about secret
      // handling, so keep the content in the wrapper's tree.
      global: { stubs: { Teleport: true } },
      props: { customerId: 'customer-1', clusterId: 'cluster-1' },
    });
    await flushPromises();

    expect(wrapper.text()).toContain('plaintext-token');
    expect(wrapper.find('button:not([disabled])').exists()).toBe(true);
    expect(JSON.stringify(useOperatorStore().$state)).not.toContain('plaintext-token');
    expect(window.location.href).not.toContain('plaintext-token');
    expect(JSON.stringify(window.localStorage)).not.toContain('plaintext-token');
    expect(JSON.stringify(window.sessionStorage)).not.toContain('plaintext-token');

    const closeButton = wrapper.findAll('button').find((button) => button.text().includes('关闭并忘记'));
    expect(closeButton?.attributes('disabled')).toBeDefined();
    await wrapper.find('input[type="checkbox"]').setValue(true);
    // Re-query: the footer/body button lives inside AppDialog's slot, so the
    // re-render after the checkbox replaces the node — and the captured one was
    // still the disabled instance.
    const enabledClose = wrapper.findAll('button').find((button) => button.text().includes('关闭并忘记'))!;
    expect(enabledClose.attributes('disabled')).toBeUndefined();
    await enabledClose.trigger('click');

    expect(wrapper.emitted('close')).toHaveLength(1);
    expect(wrapper.text()).not.toContain('plaintext-token');
    expect(JSON.stringify(useOperatorStore().$state)).not.toContain('plaintext-token');
    expect(vi.mocked(navigator.clipboard.writeText)).not.toHaveBeenCalled();
  });

  it('keeps the modal open after a failed discard so the user can retry manually', async () => {
    vi.mocked(revokePendingEnrollmentToken).mockRejectedValueOnce(new TypeError('Failed to fetch'));
    const wrapper = mount(EnrollmentTokenModal, {
      // The overlay is teleported by AppDialog; this test is about secret
      // handling, so keep the content in the wrapper's tree.
      global: { stubs: { Teleport: true } },
      props: { customerId: 'customer-1', clusterId: 'cluster-1' },
    });
    await flushPromises();

    await wrapper.findAll('input[type="checkbox"]')[1]?.setValue(true);
    const discardButton = wrapper.findAll('button').find((button) => button.text().includes('丢弃令牌'));
    await discardButton?.trigger('click');
    await flushPromises();

    expect(wrapper.emitted('close')).toBeUndefined();
    expect(wrapper.text()).toContain('plaintext-token');
    expect(wrapper.text()).toContain('Failed to fetch');
    expect(revokePendingEnrollmentToken).toHaveBeenCalledTimes(1);
  });

  /*
   * UX-039. What Escape must not destroy is the plaintext; the failure branch has
   * none on screen and already offers Cancel, so the keyboard gets the same exit.
   */
  it('closes on Escape when generation failed and no secret is on screen (UX-039)', async () => {
    vi.mocked(createEnrollmentToken).mockRejectedValueOnce(new TypeError('Failed to fetch'));
    const wrapper = mount(EnrollmentTokenModal, {
      global: { stubs: { Teleport: true } },
      props: { customerId: 'customer-1', clusterId: 'cluster-1' },
    });
    await flushPromises();

    expect(wrapper.text()).toContain(t('operator.token.failedTitle'));
    expect(wrapper.text()).not.toContain('plaintext-token');

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();

    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('refuses Escape while the one-time token is on screen (UX-039)', async () => {
    const wrapper = mount(EnrollmentTokenModal, {
      global: { stubs: { Teleport: true } },
      props: { customerId: 'customer-1', clusterId: 'cluster-1' },
    });
    await flushPromises();

    expect(wrapper.text()).toContain('plaintext-token');

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();

    expect(wrapper.emitted('close')).toBeUndefined();
    expect(wrapper.text()).toContain('plaintext-token');
  });

  /*
   * UX-009. The clipboard write fails for reasons the page cannot argue with: an
   * insecure origin has no `navigator.clipboard` at all (measured 2026-10-17:
   * `undefined` on an http:// page, an object on http://127.0.0.1 — see the guard in the
   * component), and a secure one still rejects while the document is unfocused or the
   * permission is denied. The old handler awaited the write with no catch, so the click
   * looked like a copy that worked.
   */
  it('reports a clipboard write the browser refused (UX-009)', async () => {
    vi.mocked(navigator.clipboard.writeText).mockRejectedValueOnce(
      new DOMException('Document is not focused.', 'NotAllowedError'),
    );
    const wrapper = mount(EnrollmentTokenModal, {
      global: { stubs: { Teleport: true } },
      props: { customerId: 'customer-1', clusterId: 'cluster-1' },
    });
    await flushPromises();

    await wrapper.findAll('button').find((button) => button.text() === t('operator.token.copy'))!.trigger('click');
    await flushPromises();

    const notice = wrapper.find('[data-testid="enrollment-copy-notice"]');
    expect(notice.exists()).toBe(true);
    // Assertive, not polite: the user must know the clipboard was not written before
    // closing the dialog and losing the plaintext.
    expect(notice.attributes('role')).toBe('alert');
    expect(notice.text()).toContain(t('operator.token.copyFailed'));
    // The fallback the notice points at: the plaintext is still on screen to select.
    expect(wrapper.text()).toContain('plaintext-token');
    expect(wrapper.emitted('close')).toBeUndefined();
  });

  it('reports the missing clipboard API of an insecure origin (UX-009)', async () => {
    const descriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
    try {
      const wrapper = mount(EnrollmentTokenModal, {
        global: { stubs: { Teleport: true } },
        props: { customerId: 'customer-1', clusterId: 'cluster-1' },
      });
      await flushPromises();

      await wrapper.findAll('button').find((button) => button.text() === t('operator.token.copy'))!.trigger('click');
      await flushPromises();

      const notice = wrapper.find('[data-testid="enrollment-copy-notice"]');
      expect(notice.attributes('role')).toBe('alert');
      expect(notice.text()).toContain(t('operator.token.copyFailed'));
    } finally {
      if (descriptor) Object.defineProperty(navigator, 'clipboard', descriptor);
      else Reflect.deleteProperty(navigator, 'clipboard');
    }
  });

  it('confirms a successful copy with a polite status notice (UX-009)', async () => {
    const wrapper = mount(EnrollmentTokenModal, {
      global: { stubs: { Teleport: true } },
      props: { customerId: 'customer-1', clusterId: 'cluster-1' },
    });
    await flushPromises();

    await wrapper.findAll('button').find((button) => button.text() === t('operator.token.copy'))!.trigger('click');
    await flushPromises();

    const notice = wrapper.find('[data-testid="enrollment-copy-notice"]');
    expect(notice.attributes('role')).toBe('status');
    expect(notice.text()).toContain(t('operator.token.copied'));
    expect(vi.mocked(navigator.clipboard.writeText)).toHaveBeenCalledWith('plaintext-token');
  });

  /*
   * TASK-389. The generated install command was not directly executable: the console
   * substituted the enrollment token but left the gateway placeholder in place, so the
   * customer had to notice it and fill it in by hand. The response already carries the
   * control plane's advertised address (`gateway.advertise_url`).
   */
  it('arms the gateway placeholder from the server-advertised endpoint (TASK-389)', async () => {
    const wrapper = await mountModal();

    // The dialog names the deployment fact the command depends on instead of leaving it
    // buried next to the template version.
    expect(wrapper.text()).toContain(t('operator.token.gatewayAddress'));
    expect(wrapper.find('[data-testid="enrollment-gateway"]').text()).toBe(GATEWAY);
    expect(wrapper.find('[data-testid="enrollment-gateway-missing"]').exists()).toBe(false);

    const command = wrapper.find('pre').text();
    expect(command).not.toContain('<CONTROL_PLANE_OPERATOR_GATEWAY>');
    expect(command).not.toContain('${ENROLLMENT_TOKEN}');
    expect(command).toContain('plaintext-token');
    expect(command).toContain(`--orchestrator-addr '${GATEWAY}'`);
    // The instruction to replace the placeholder is obsolete once the console has
    // replaced it: left in, it would ask the customer to overwrite a correct value.
    expect(command).not.toContain('Replace <CONTROL_PLANE_OPERATOR_GATEWAY>');

    await wrapper
      .findAll('button')
      .find((button) => button.text() === t('operator.token.copyCommand'))!
      .trigger('click');
    await flushPromises();

    // The clipboard receives exactly the armed command on screen, not a partial one.
    expect(vi.mocked(navigator.clipboard.writeText)).toHaveBeenCalledWith(command);
  });

  it('drops the endpoint trailing slash so the command has no double slash (TASK-389)', async () => {
    vi.mocked(createEnrollmentToken).mockResolvedValueOnce({ ...result, operatorEndpoint: 'https://x:8444/' });
    const wrapper = await mountModal();

    expect(wrapper.find('[data-testid="enrollment-gateway"]').text()).toBe('https://x:8444');
    const command = wrapper.find('pre').text();
    expect(command).toContain("--orchestrator-addr 'https://x:8444'");
    expect(command).not.toMatch(/8444\//);
  });

  it('keeps the placeholder and warns when the endpoint is missing (TASK-389)', async () => {
    vi.mocked(createEnrollmentToken).mockResolvedValueOnce({ ...result, operatorEndpoint: '' });
    const wrapper = await mountModal();

    const command = wrapper.find('pre').text();
    // Fail closed: an empty endpoint must not fabricate a command. The placeholder stays,
    // and the dialog says why instead of presenting the command as ready to run.
    expect(command).toContain("'<CONTROL_PLANE_OPERATOR_GATEWAY>'");
    expect(command).toContain('plaintext-token');
    expect(command).toContain(`# ${t('operator.token.gatewayRemediation')}`);

    expect(wrapper.find('[data-testid="enrollment-gateway"]').text()).toBe(t('operator.token.gatewayMissing'));
    const notice = wrapper.find('[data-testid="enrollment-gateway-missing"]');
    expect(notice.exists()).toBe(true);
    expect(notice.attributes('role')).toBe('alert');
    expect(notice.text()).toBe(t('operator.token.gatewayMissingWarning'));
    expect(notice.text()).toContain('<CONTROL_PLANE_OPERATOR_GATEWAY>');
  });

  /*
   * TASK-389 review block. The server still advertises the control-plane fixture
   * address (`cmd/orchestrator/main.go`), not one a customer cluster can reach, and the
   * original change removed the "replace this" instruction. Arming the command with
   * that value would be worse than the old placeholder: it runs and then cannot connect,
   * with nothing telling the customer what is missing. Each known-unreachable shape
   * therefore takes the same fail-closed branch as an empty endpoint.
   */
  it.each(UNREACHABLE_GATEWAYS)(
    'keeps the placeholder and warns when the advertised gateway is unreachable: $endpoint',
    async ({ endpoint, reason }) => {
      const wrapper = await mountWithEndpoint(endpoint);
      expectGatewayFailsClosed(wrapper, endpoint, reason);
    },
  );

  // The classification must key off URL structure, not a substring: a public host that
  // merely starts with the Service name is still a reachable gateway.
  it('arms a public endpoint that only looks cluster-internal (TASK-389)', async () => {
    const endpoint = 'https://operator.example.com:8444';
    const wrapper = await mountWithEndpoint(endpoint);

    const command = wrapper.find('pre').text();
    expect(command).toContain(`--orchestrator-addr '${endpoint}'`);
    expect(command).toContain('plaintext-token');
    expect(command).not.toContain(`# ${t('operator.token.gatewayRemediation')}`);
    expect(wrapper.find('[data-testid="enrollment-gateway"]').text()).toBe(endpoint);
    expect(wrapper.find('[data-testid="enrollment-gateway-missing"]').exists()).toBe(false);
  });
});
