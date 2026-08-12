import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import EnrollmentTokenModal from './EnrollmentTokenModal.vue';
import { useOperatorStore } from '@/stores/operator';
import { createEnrollmentToken, revokePendingEnrollmentToken } from '@/connect/operator-api';
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
      props: { customerId: 'customer-1', clusterId: 'cluster-1' },
    });
    await flushPromises();

    expect(wrapper.text()).toContain('plaintext-token');
    expect(wrapper.find('button:not([disabled])').exists()).toBe(true);
    expect(JSON.stringify(useOperatorStore().$state)).not.toContain('plaintext-token');
    expect(window.location.href).not.toContain('plaintext-token');
    expect(JSON.stringify(window.localStorage)).not.toContain('plaintext-token');
    expect(JSON.stringify(window.sessionStorage)).not.toContain('plaintext-token');

    const closeButton = wrapper.findAll('button').find((button) => button.text().includes('Close and forget'));
    expect(closeButton?.attributes('disabled')).toBeDefined();
    await wrapper.find('input[type="checkbox"]').setValue(true);
    await closeButton?.trigger('click');

    expect(wrapper.emitted('close')).toHaveLength(1);
    expect(wrapper.text()).not.toContain('plaintext-token');
    expect(JSON.stringify(useOperatorStore().$state)).not.toContain('plaintext-token');
    expect(vi.mocked(navigator.clipboard.writeText)).not.toHaveBeenCalled();
  });

  it('keeps the modal open after a failed discard so the user can retry manually', async () => {
    vi.mocked(revokePendingEnrollmentToken).mockRejectedValueOnce(new TypeError('Failed to fetch'));
    const wrapper = mount(EnrollmentTokenModal, {
      props: { customerId: 'customer-1', clusterId: 'cluster-1' },
    });
    await flushPromises();

    await wrapper.findAll('input[type="checkbox"]')[1]?.setValue(true);
    const discardButton = wrapper.findAll('button').find((button) => button.text().includes('Discard token'));
    await discardButton?.trigger('click');
    await flushPromises();

    expect(wrapper.emitted('close')).toBeUndefined();
    expect(wrapper.text()).toContain('plaintext-token');
    expect(wrapper.text()).toContain('Failed to fetch');
    expect(revokePendingEnrollmentToken).toHaveBeenCalledTimes(1);
  });
});
