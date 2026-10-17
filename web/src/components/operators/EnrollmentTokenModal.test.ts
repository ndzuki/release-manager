import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import EnrollmentTokenModal from './EnrollmentTokenModal.vue';
import { useOperatorStore } from '@/stores/operator';
import { createEnrollmentToken, revokePendingEnrollmentToken } from '@/connect/operator-api';
import { t } from '@/i18n/messages';
import type * as OperatorApi from '@/connect/operator-api';

vi.mock('@/connect/operator-api', async (importOriginal) => {
  const original = await importOriginal<typeof OperatorApi>();
  return {
    ...original,
    createEnrollmentToken: vi.fn(),
    revokePendingEnrollmentToken: vi.fn(),
  };
});

const result = {
  token: 'plaintext-token',
  expiresAt: '2026-07-27T13:00:00.000Z',
  customerId: 'customer-1',
  clusterId: 'cluster-1',
  clusterName: 'Staging',
  operatorEndpoint: 'https://operator.example.com',
  installCommandTemplateVersion: 'v1',
  installCommandTemplate: 'release-operator --enrollment-token ${ENROLLMENT_TOKEN}',
};

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
});
