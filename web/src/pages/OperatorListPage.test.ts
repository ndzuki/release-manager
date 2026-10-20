import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ref } from 'vue';
import { createMemoryHistory, createRouter } from 'vue-router';
import OperatorListPage from './OperatorListPage.vue';
import * as api from '@/connect/operator-api';
import * as polling from '@/composables/useOperatorPolling';
import { t } from '@/i18n/messages';

/*
 * TASK-330 (AC-330-03): the page-level half of the polling notice's contract.
 *
 * The composable's state machine is covered by `useOperatorPolling.test.ts`; what a
 * composable test cannot see is the WIRING, and that is where the two refresh paths
 * deliberately part ways: the header's own refresh button re-reads the data but must
 * NOT re-arm the automatic chain, while only the notice's `resume` does (the notice is
 * rendered by a sibling test). That separation is the reason the notice's copy speaks
 * about the automatic chain and never claims the displayed data went stale.
 */

vi.mock('@/connect/operator-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listOperators: vi.fn() };
});

vi.mock('@/composables/useOperatorPolling', async (importOriginal) => {
  const original = await importOriginal<typeof polling>();
  return { ...original, useOperatorPolling: vi.fn() };
});

const mockedList = vi.mocked(api.listOperators);
const mockedPolling = vi.mocked(polling.useOperatorPolling);

const emptyPage = { operators: [], nextPageToken: null, totalCount: 0, heartbeatIntervalSeconds: 15 };

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters/:clusterId/operators', name: 'OperatorList', component: OperatorListPage },
      { path: '/', name: 'Home', component: { template: '<div />' } },
    ],
  });
  await router.push('/customers/c1/clusters/k1/operators');
  await router.isReady();
  const wrapper = mount(OperatorListPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

describe('OperatorListPage polling notice wiring', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.clearAllMocks();
  });

  it('refreshes the data from the header button without re-arming the stopped chain', async () => {
    const stopped = ref(true);
    const resume = vi.fn();
    mockedPolling.mockReturnValue({ stopped, resume });
    mockedList.mockResolvedValue(emptyPage);

    const wrapper = await mountPage();
    // The stop is the state the notice renders; the page must never swallow it.
    expect(wrapper.find('[data-testid="operator-polling-stopped"]').exists()).toBe(true);

    await wrapper.get('.actions button').trigger('click');
    await flushPromises();

    // The header button is a real refresh (asserted below), but it is not a resume: the
    // automatic chain stays stopped and the notice that says so stays up. Binding this
    // button to `resume` would collapse the two paths and make the notice disappear on a
    // refresh that never re-armed anything.
    expect(resume).not.toHaveBeenCalled();
    expect(stopped.value).toBe(true);
    expect(wrapper.find('[data-testid="operator-polling-stopped"]').exists()).toBe(true);
    expect(mockedList).toHaveBeenCalledTimes(2);

    // The one affordance that restores the chain is the notice's own button.
    await wrapper.get('[data-testid="operator-polling-resume"]').trigger('click');
    expect(resume).toHaveBeenCalledTimes(1);
  });
});

/*
 * W2 (TASK-394): the hand-rolled header had the list name in the eyebrow and the
 * descriptor in the h1. The swap is a semantic fix, so it is asserted against the
 * catalog values rather than against the markup shape alone.
 */
describe('OperatorListPage header (W2)', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.clearAllMocks();
    mockedPolling.mockReturnValue({ stopped: ref(false), resume: vi.fn() });
  });

  it('uses the list name as the title and the history line as the subtitle', async () => {
    mockedList.mockResolvedValue(emptyPage);
    const wrapper = await mountPage();

    const header = wrapper.get('.page-header');
    expect(header.get('h1').text()).toBe(t('operator.list.title'));
    expect(header.get('.page-header__subtitle').text()).toBe(t('operator.list.subtitle'));
    // The reversed header put the list name in the eyebrow, so there is no separate
    // category string to keep above the title: the eyebrow must be absent, not a copy
    // of the title.
    expect(header.find('.page-header__eyebrow').exists()).toBe(false);
    // The explanation the old third line carried is still rendered, exactly once.
    expect(wrapper.get('.page__intro').text()).toBe(t('operator.list.description'));
  });
});
