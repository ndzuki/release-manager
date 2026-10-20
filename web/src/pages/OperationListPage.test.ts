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

async function mountPage(query: Record<string, string> = {}) {
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
  await router.push({ path: PATH, query });
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

/*
 * TASK-394 (consistency wave 5): the hand-rolled `<nav aria-label="面包屑">` + eyebrow/h1
 * header became PageHeader. Two invariants carried over: the release NAME is the subtitle
 * (the definition id is never printed as raw text), and the trail is built from NAMES —
 * a level whose label would be the route identifier is dropped instead of shown.
 */
describe('OperationListPage header (TASK-394)', () => {
  it('renders the release name as the subtitle and names in the crumb trail', async () => {
    mockedList.mockResolvedValue({ operations: [], nextCursor: '' });

    const wrapper = await mountPage({
      customerName: 'Customer One',
      clusterName: 'Cluster One',
      releaseName: 'team/app',
    });

    expect(wrapper.get('h1').text()).toBe('操作历史');
    expect(wrapper.get('.page-header__eyebrow').text()).toBe('Release 操作');
    expect(wrapper.get('.page-header__subtitle').text()).toBe('team/app');
    // The release definition id left the header entirely; the name identifies the release.
    expect(wrapper.get('.page-header').text()).not.toContain(RELEASE);

    const links = wrapper.findAll('nav a');
    expect(links.map((link) => link.text())).toEqual(['客户', 'Customer One', 'Cluster One']);
    expect(links[0]!.attributes('href')).toBe('/customers');
    expect(links[1]!.attributes('href')).toBe(`/customers/${CUSTOMER}/clusters`);
    expect(links[2]!.attributes('href')).toContain(`/customers/${CUSTOMER}/clusters/${CLUSTER}/releases`);
    // The last crumb is this page: never a link, marked aria-current.
    expect(wrapper.get('nav [aria-current="page"]').text()).toBe('操作历史');
  });

  it('falls back to a copyable definition hash when the URL carries no release name', async () => {
    mockedList.mockResolvedValue({ operations: [], nextCursor: '' });

    const wrapper = await mountPage();

    // No subtitle to invent: the definition id surfaces as a truncated, copyable hash.
    expect(wrapper.find('.page-header__subtitle').exists()).toBe(false);
    expect(wrapper.get('.copyable-hash__value').attributes('title')).toBe(RELEASE);
    // Identifier-level crumbs are dropped, not printed: 客户 + this page only.
    expect(wrapper.findAll('nav li')).toHaveLength(2);
    expect(wrapper.get('nav').text()).not.toContain(CUSTOMER);
    expect(wrapper.get('nav').text()).not.toContain(CLUSTER);
  });
});
