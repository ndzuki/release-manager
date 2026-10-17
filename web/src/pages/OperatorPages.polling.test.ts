import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import OperatorDetailPage from './OperatorDetailPage.vue';
import OperatorListPage from './OperatorListPage.vue';
import { OPERATOR_POLLING_RETRY_BASE_MS } from '@/composables/useOperatorPolling';
import { getOperator, listOperators } from '@/connect/operator-api';
import type * as OperatorApi from '@/connect/operator-api';
import { t } from '@/i18n/messages';
import { useAuthStore } from '@/stores/auth';
import type { OperatorDetail, OperatorPage } from '@/types/operator';

vi.mock('@/connect/operator-api', async (importOriginal) => {
  const original = await importOriginal<typeof OperatorApi>();
  return { ...original, getOperator: vi.fn(), listOperators: vi.fn() };
});

/*
 * TASK-325 (AC-325-05) at the page boundary: the composable's `stopped` flag has to
 * reach a real page and turn into a live region with a working restore path. The
 * composable's own unit tests pin the state machine; this file pins the wiring, which is
 * exactly the half that stays invisible when it is missing.
 */
const LIST_PAGE: OperatorPage = {
  operators: [],
  nextPageToken: null,
  totalCount: 0,
  heartbeatIntervalSeconds: 15,
};

const DETAIL: OperatorDetail = {
  id: 'operator-1',
  name: 'operator-one',
  customerId: 'customer-1',
  clusterId: 'cluster-1',
  clusterName: 'Staging',
  lifecycleStatus: 'active',
  sessionStatus: 'offline',
  sessionStatusReason: 'heartbeat_timeout',
  lastHeartbeat: '2026-07-20T01:02:03.000Z',
  registeredAt: '2026-07-01T00:00:00.000Z',
  supersededAt: null,
  revokedAt: null,
  supersededBy: null,
  revokeReason: null,
  instanceId: null,
  version: null,
  capabilities: {},
};

function authenticate(role: string): void {
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: {
      $typeName: 'auth.v1.SessionUser',
      id: 'user-1',
      username: role,
      roles: [role],
      activeOrgId: 'org-1',
    },
  });
}

async function mountListPage(): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters/:clusterId/operators', name: 'OperatorList', component: OperatorListPage },
      { path: '/customers/:customerId/clusters/:clusterId/operators/new', name: 'OperatorEnroll', component: { template: '<div />' } },
    ],
  });
  await router.push('/customers/customer-1/clusters/cluster-1/operators');
  await router.isReady();
  const wrapper = mount(OperatorListPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

async function mountDetailPage(): Promise<VueWrapper> {
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

/*
 * Drives the automatic chain from a fresh mount to a spent budget: the steady interval
 * (30s for the 15s heartbeat) elapses first, then the 5s/10s/20s/40s backoffs. Advancing
 * less than the full chain must leave the notice absent, which the callers assert.
 */
async function spendPollingBudget(): Promise<void> {
  await vi.advanceTimersByTimeAsync(30_000);
  await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS);
  await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 2);
  await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 4);
  await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 8);
}

const STOPPED = '[data-testid="operator-polling-stopped"]';
const RESUME = '[data-testid="operator-polling-resume"]';

beforeEach(() => {
  setActivePinia(createPinia());
  vi.mocked(listOperators).mockReset();
  vi.mocked(getOperator).mockReset();
  vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
});

afterEach(() => {
  vi.useRealTimers();
});

describe('operator pages: stopped-poll notice', () => {
  it('surfaces the spent budget on the list page and restores the chain from the notice', async () => {
    authenticate('viewer');
    let loads = 0;
    let failing = false;
    vi.mocked(listOperators).mockImplementation(async () => {
      loads += 1;
      if (failing) throw new Error('poll failed');
      return LIST_PAGE;
    });

    vi.useFakeTimers();
    const wrapper = await mountListPage();

    // The first load succeeds, so the page learns the heartbeat and starts the steady poll.
    expect(loads).toBe(1);
    expect(wrapper.find(STOPPED).exists()).toBe(false);

    failing = true;
    await vi.advanceTimersByTimeAsync(30_000);
    expect(loads).toBe(2);
    // Four failures in, the budget is not spent yet: the notice must stay away.
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 2);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 4);
    expect(loads).toBe(5);
    expect(wrapper.find(STOPPED).exists()).toBe(false);

    // The fifth failure spends the budget: automatic refresh has stopped, and the page says so.
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 8);
    expect(loads).toBe(6);
    const notice = wrapper.get(STOPPED);
    expect(notice.attributes('role')).toBe('status');
    expect(notice.text()).toContain(t('operator.polling.stopped'));
    expect(wrapper.get(RESUME).text()).toBe(t('operator.polling.resume'));

    // The restore path really restarts the chain: a steady interval polls again.
    failing = false;
    await wrapper.get(RESUME).trigger('click');
    await flushPromises();
    expect(loads).toBe(7);
    expect(wrapper.find(STOPPED).exists()).toBe(false);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(loads).toBe(8);
    expect(wrapper.find(STOPPED).exists()).toBe(false);
  });

  it('surfaces the same notice on the detail page and restores its chain', async () => {
    authenticate('viewer');
    let loads = 0;
    let failing = false;
    vi.mocked(getOperator).mockImplementation(async () => {
      loads += 1;
      if (failing) throw new Error('poll failed');
      return { operator: DETAIL, heartbeatIntervalSeconds: 15 };
    });

    vi.useFakeTimers();
    const wrapper = await mountDetailPage();
    expect(loads).toBe(1);

    failing = true;
    await spendPollingBudget();
    expect(loads).toBe(6);
    const notice = wrapper.get(STOPPED);
    expect(notice.attributes('role')).toBe('status');
    expect(notice.text()).toContain(t('operator.polling.stopped'));

    failing = false;
    await wrapper.get(RESUME).trigger('click');
    await flushPromises();
    expect(loads).toBe(7);
    expect(wrapper.find(STOPPED).exists()).toBe(false);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(loads).toBe(8);
  });
});
