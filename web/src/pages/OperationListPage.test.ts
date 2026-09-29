import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import OperationListPage from './OperationListPage.vue';
import * as api from '@/connect/operation-api';

vi.mock('@/connect/operation-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listOperations: vi.fn() };
});

const mockedList = vi.mocked(api.listOperations);

const CUSTOMER = 'cust-1';
const CLUSTER = 'cluster-1';
const RELEASE = 'def-1';
const ROUTE_PATH = '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations';
const PATH = `/customers/${CUSTOMER}/clusters/${CLUSTER}/releases/${RELEASE}/operations`;

function summary(id: string, overrides: Partial<api.OperationSummaryItem> = {}): api.OperationSummaryItem {
  return {
    operationId: id,
    operationType: 'UPGRADE',
    state: 'succeeded',
    revision: 3,
    createdAt: '2026-09-28T08:00:00.000Z',
    ...overrides,
  };
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      // Placeholders matter: with a literal path the page would see no params.
      { path: ROUTE_PATH, name: 'OperationList', component: OperationListPage },
      { path: '/customers', name: 'CustomerList', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters', name: 'ClusterList', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters/:clusterId/releases', name: 'ReleaseInventory', component: { template: '<div />' } },
      { path: `${ROUTE_PATH}/new`, name: 'OperationCreate', component: { template: '<div />' } },
      { path: `${ROUTE_PATH}/:operationId`, name: 'OperationDetail', component: { template: '<div />' } },
    ],
  });
  await router.push(PATH);
  await router.isReady();
  const wrapper = mount(OperationListPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  mockedList.mockReset();
});

describe('OperationListPage', () => {
  it('renders the release operation history with state labels and detail links', async () => {
    mockedList.mockResolvedValue({
      operations: [summary('op-1'), summary('op-2', { state: 'failed', operationType: 'ROLLBACK', revision: 5 })],
      nextCursor: '',
    });

    const wrapper = await mountPage();

    expect(mockedList).toHaveBeenCalledWith(RELEASE, expect.objectContaining({ statusFilter: '', cursor: '' }));
    expect(wrapper.find('[data-testid="operation-row-op-1"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="operation-row-op-1"]').text()).toContain('成功');
    expect(wrapper.find('[data-testid="operation-row-op-2"]').text()).toContain('失败');
    const link = wrapper.find('[data-testid="operation-row-op-2"] a');
    expect(link.attributes('href')).toContain('/operations/op-2');
  });

  it('re-queries with the selected status filter', async () => {
    mockedList.mockResolvedValue({ operations: [summary('op-1')], nextCursor: '' });
    const wrapper = await mountPage();
    mockedList.mockClear();
    mockedList.mockResolvedValue({ operations: [summary('op-3', { state: 'running' })], nextCursor: '' });

    await wrapper.get('select[name="status"]').setValue('running');
    await flushPromises();

    expect(mockedList).toHaveBeenCalledWith(RELEASE, expect.objectContaining({ statusFilter: 'running' }));
    expect(wrapper.find('[data-testid="operation-row-op-3"]').text()).toContain('执行中');
  });

  it('appends the next page through the cursor', async () => {
    mockedList.mockResolvedValueOnce({ operations: [summary('op-1')], nextCursor: 'cursor-1' });
    const wrapper = await mountPage();
    expect(wrapper.text()).toContain('加载更多');

    mockedList.mockResolvedValueOnce({ operations: [summary('op-2')], nextCursor: '' });
    await wrapper.get('.operation-list__more').trigger('click');
    await flushPromises();

    expect(mockedList).toHaveBeenLastCalledWith(RELEASE, expect.objectContaining({ cursor: 'cursor-1' }));
    expect(wrapper.find('[data-testid="operation-row-op-1"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="operation-row-op-2"]').exists()).toBe(true);
    expect(wrapper.text()).not.toContain('加载更多');
  });

  // UX-010 discipline: a failed request must never also read as "no operations".
  it('shows the error state and no empty state when the first load fails', async () => {
    mockedList.mockRejectedValue(new ConnectError('unavailable', Code.Unavailable));

    const wrapper = await mountPage();

    expect(wrapper.text()).toContain('操作历史加载失败');
    expect(wrapper.text()).not.toContain('暂无操作记录');
  });

  it('drops rows from a previous filter when the new filter query fails', async () => {
    mockedList.mockResolvedValueOnce({ operations: [summary('op-1')], nextCursor: '' });
    const wrapper = await mountPage();

    mockedList.mockRejectedValueOnce(new ConnectError('unavailable', Code.Unavailable));
    await wrapper.get('select[name="status"]').setValue('failed');
    await flushPromises();

    // The old rows belonged to "全部状态" and must not stand in for a failed
    // "failed" query.
    expect(wrapper.find('[data-testid="operation-row-op-1"]').exists()).toBe(false);
    expect(wrapper.text()).toContain('操作历史加载失败');
  });

  it('keeps the loaded rows and reports a refresh failure inline', async () => {
    mockedList.mockResolvedValueOnce({ operations: [summary('op-1')], nextCursor: '' });
    const wrapper = await mountPage();

    mockedList.mockRejectedValueOnce(new ConnectError('unavailable', Code.Unavailable));
    // happy-dom does not synthesise a form submit from a button click.
    await wrapper.get('form').trigger('submit');
    await flushPromises();

    expect(wrapper.find('[data-testid="operation-row-op-1"]').exists()).toBe(true);
    expect(wrapper.find('.operation-list__warning').exists()).toBe(true);
    expect(wrapper.text()).not.toContain('暂无操作记录');
  });

  // connect-surface §分页: an expired cursor resets THAT list and keeps the form.
  it('recovers from an expired cursor by reloading the first page with the filter kept', async () => {
    mockedList.mockResolvedValueOnce({ operations: [summary('op-1')], nextCursor: 'stale-cursor' });
    const wrapper = await mountPage();
    // the filter change re-queries and yields the cursor we are about to expire
    mockedList.mockResolvedValueOnce({ operations: [summary('op-1')], nextCursor: 'stale-cursor' });
    await wrapper.get('select[name="status"]').setValue('succeeded');
    await flushPromises();
    mockedList.mockClear();

    // "load more" hits an expired cursor…
    mockedList.mockRejectedValueOnce(new ConnectError('expired', Code.InvalidArgument, { 'X-Reason-Code': 'invalid_cursor' }));
    // …and the page immediately re-queries from the first page with the filter.
    mockedList.mockResolvedValueOnce({ operations: [summary('op-9')], nextCursor: '' });
    await wrapper.get('.operation-list__more').trigger('click');
    await flushPromises();

    expect(mockedList).toHaveBeenNthCalledWith(1, RELEASE, expect.objectContaining({ cursor: 'stale-cursor' }));
    expect(mockedList).toHaveBeenNthCalledWith(2, RELEASE, expect.objectContaining({ cursor: '', statusFilter: 'succeeded' }));
    expect(wrapper.find('.operation-list__notice').exists()).toBe(true);
    expect(wrapper.find('[data-testid="operation-row-op-9"]').exists()).toBe(true);
  });

  it('shows the empty state only on a successful empty response', async () => {
    mockedList.mockResolvedValue({ operations: [], nextCursor: '' });

    const wrapper = await mountPage();

    expect(wrapper.text()).toContain('暂无操作记录');
    expect(wrapper.text()).not.toContain('操作历史加载失败');
  });
});
