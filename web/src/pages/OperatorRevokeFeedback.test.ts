import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import OperatorDetailPage from './OperatorDetailPage.vue';
import OperatorListPage from './OperatorListPage.vue';
import * as operatorApi from '@/connect/operator-api';
import type * as OperatorApi from '@/connect/operator-api';
import { useAuthStore } from '@/stores/auth';
import type { OperatorDetail, OperatorSummary } from '@/types/operator';

/*
 * W10 (UX-041): revoking an operator is destructive and irreversible, and both pages
 * used to end the flow by only re-rendering the row — the confirmation dialog closed
 * and nothing said the revocation had gone through. These cases pin the success toast
 * on both entry points, and that the dialog's own failure path (which renders inside
 * the dialog) does not also raise a success toast.
 *
 * Kept in its own file so the existing `OperatorPages.test.ts` mock surface stays
 * untouched: this one mocks the RPC the store actually calls.
 */
vi.mock('@/connect/operator-api', async (importOriginal) => {
  const original = await importOriginal<typeof OperatorApi>();
  return {
    ...original,
    getOperator: vi.fn(),
    listOperators: vi.fn(),
    revokeOperator: vi.fn(),
  };
});

const mockedList = vi.mocked(operatorApi.listOperators);
const mockedGet = vi.mocked(operatorApi.getOperator);
const mockedRevoke = vi.mocked(operatorApi.revokeOperator);

function summary(overrides: Partial<OperatorSummary> = {}): OperatorSummary {
  return {
    id: 'operator-1',
    name: 'operator-one',
    customerId: 'customer-1',
    clusterId: 'cluster-1',
    clusterName: 'Staging',
    lifecycleStatus: 'active',
    sessionStatus: 'online',
    sessionStatusReason: null,
    lastHeartbeat: '2026-10-18T03:00:00.000Z',
    registeredAt: '2026-10-01T00:00:00.000Z',
    supersededAt: null,
    revokedAt: null,
    ...overrides,
  };
}

function detail(overrides: Partial<OperatorDetail> = {}): OperatorDetail {
  return {
    ...summary(),
    supersededBy: null,
    revokeReason: null,
    instanceId: null,
    version: null,
    capabilities: {},
    ...overrides,
  };
}

function authenticateAdmin(): void {
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: {
      $typeName: 'auth.v1.SessionUser',
      id: 'admin-1',
      username: 'admin',
      roles: ['release_admin'],
      activeOrgId: 'org-1',
    },
  });
}

/** The confirm button stays disabled until the reason passes validation. */
function fillReason(reason: string): void {
  const panel = document.querySelector<HTMLElement>('.app-dialog__panel');
  if (!panel) throw new Error('confirmation dialog not rendered');
  const textarea = panel.querySelector('textarea');
  if (!textarea) throw new Error('confirmation dialog has no reason field');
  textarea.value = reason;
  textarea.dispatchEvent(new Event('input'));
}

function clickDialogButton(label: string): void {
  const panel = document.querySelector<HTMLElement>('.app-dialog__panel');
  if (!panel) throw new Error('confirmation dialog not rendered');
  const button = Array.from(panel.querySelectorAll('button')).find((element) => element.textContent?.trim() === label);
  if (!button) throw new Error(`dialog button not found: ${label}`);
  button.click();
}

/** The revoke dialog requires a reason of at least 5 characters. */
const REASON = 'compromised credential';

beforeEach(() => {
  setActivePinia(createPinia());
  authenticateAdmin();
  mockedList.mockReset().mockResolvedValue({
    operators: [summary()],
    nextPageToken: null,
    totalCount: 1,
    heartbeatIntervalSeconds: 15,
  });
  mockedGet.mockReset().mockResolvedValue({ operator: detail(), heartbeatIntervalSeconds: 15 });
  mockedRevoke.mockReset().mockResolvedValue({
    operator: summary({ lifecycleStatus: 'revoked', revokedAt: '2026-10-18T04:00:00.000Z' }),
    changed: true,
  });
});

async function mountList() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters/:clusterId/operators', name: 'OperatorList', component: OperatorListPage },
      { path: '/customers/:customerId/clusters/:clusterId/operators/new', name: 'OperatorEnroll', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters/:clusterId/operators/:operatorId', name: 'OperatorDetail', component: { template: '<div />' } },
    ],
  });
  await router.push('/customers/customer-1/clusters/cluster-1/operators');
  await router.isReady();
  const wrapper = mount(OperatorListPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

async function mountDetail() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters/:clusterId/operators', name: 'OperatorList', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters/:clusterId/operators/:operatorId', name: 'OperatorDetail', component: OperatorDetailPage },
    ],
  });
  await router.push('/customers/customer-1/clusters/cluster-1/operators/operator-1');
  await router.isReady();
  const wrapper = mount(OperatorDetailPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

describe('operator revocation feedback (W10)', () => {
  it('announces a successful revocation from the list with a polite success toast', async () => {
    const wrapper = await mountList();
    expect(wrapper.find('[data-testid="operator-list-notice"]').exists()).toBe(false);

    await wrapper.get('.operators tbody .danger').trigger('click');
    fillReason(REASON);
    await flushPromises();
    clickDialogButton('确认撤销');
    await flushPromises();

    const notice = wrapper.get('[data-testid="operator-list-notice"]');
    expect(notice.attributes('role')).toBe('status');
    expect(notice.text()).toContain('已撤销 Operator operator-one');
    expect(mockedRevoke).toHaveBeenCalledWith('customer-1', 'cluster-1', 'operator-1', REASON);
    wrapper.unmount();
  });

  it('announces a successful revocation from the detail page', async () => {
    const wrapper = await mountDetail();
    expect(wrapper.find('[data-testid="operator-detail-notice"]').exists()).toBe(false);

    await wrapper.findAll('button').find((button) => button.text() === '撤销 Operator')?.trigger('click');
    fillReason(REASON);
    await flushPromises();
    clickDialogButton('确认撤销');
    await flushPromises();

    const notice = wrapper.get('[data-testid="operator-detail-notice"]');
    expect(notice.attributes('role')).toBe('status');
    expect(notice.text()).toContain('已撤销 Operator operator-one');
    wrapper.unmount();
  });

  it('does not claim success when the server refuses the revocation', async () => {
    mockedRevoke.mockRejectedValue(new Error('operator revoke refused'));
    const wrapper = await mountList();

    await wrapper.get('.operators tbody .danger').trigger('click');
    fillReason(REASON);
    await flushPromises();
    clickDialogButton('确认撤销');
    await flushPromises();

    // The refusal stays in the dialog (its own alert); no success toast appears.
    expect(wrapper.find('[data-testid="operator-list-notice"]').exists()).toBe(false);
    expect(mockedRevoke).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});
