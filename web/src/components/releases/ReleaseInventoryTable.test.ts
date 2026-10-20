import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import ReleaseInventoryTable from './ReleaseInventoryTable.vue';
import type { ReleaseSummary } from '@/stores/releaseInventory';

function release(overrides: Partial<ReleaseSummary> = {}): ReleaseSummary {
  return {
    releaseDefinitionId: 'def-1',
    namespace: 'ns',
    name: 'release-a',
    chart: 'chart',
    chartVersion: '1.0.0',
    revision: 4,
    status: 'active',
    valuesDigest: 'd',
    lastSyncAt: null,
    emergencyConflict: false,
    pendingConvergenceCount: 0,
    revertStatusSummary: '',
    ...overrides,
  };
}

async function mountTable(releases: ReleaseSummary[], canRollback = true) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'Home', component: { template: '<div />' } },
      { path: '/x', name: 'ValuesEditor', component: { template: '<div />' } },
      { path: '/e', name: 'EmergencyChange', component: { template: '<div />' } },
      { path: '/c', name: 'ValuesRevisionList', component: { template: '<div />' } },
      { path: '/r', name: 'OperationCreate', component: { template: '<div />' } },
      { path: '/o', name: 'OperationList', component: { template: '<div />' } },
      { path: '/rs', name: 'ReleaseInventory', component: { template: '<div />' } },
    ],
  });
  await router.push('/');
  await router.isReady();
  const wrapper = mount(ReleaseInventoryTable, {
    props: { releases, canCreateOperation: true, canRollback, customerId: 'c', clusterId: 'k' },
    global: { plugins: [router] },
  });
  return wrapper;
}

describe('ReleaseInventoryTable rollback entry (A11)', () => {
  it('offers rollback for a deployable release and emits the release upward', async () => {
    const wrapper = await mountTable([release()]);

    const button = wrapper.get('[data-testid="release-rollback-ns-release-a"]');
    await button.trigger('click');

    expect(wrapper.emitted('rollback')?.[0]?.[0]).toMatchObject({ releaseDefinitionId: 'def-1', revision: 4 });
  });

  // Nothing to roll back to on a first revision, and the server would reject
  // target_revision < 1 anyway.
  it('hides the rollback entry when the release has no earlier revision', async () => {
    const wrapper = await mountTable([release({ revision: 1 })]);

    expect(wrapper.find('[data-testid="release-rollback-ns-release-a"]').exists()).toBe(false);
  });

  it('hides the rollback entry without the write capability', async () => {
    const wrapper = await mountTable([release()], false);

    expect(wrapper.find('[data-testid="release-rollback-ns-release-a"]').exists()).toBe(false);
  });

  it('hides the rollback entry for a release without a bound definition', async () => {
    const wrapper = await mountTable([release({ releaseDefinitionId: '' })]);

    expect(wrapper.find('[data-testid="release-rollback-ns-release-a"]').exists()).toBe(false);
  });
});

/*
 * UX-073 (W15): on a narrow screen the table used to be a bare horizontal scroller.
 * The wrapper is now a labelled, keyboard-focusable region, the first column stays
 * visible while scrolling, and every cell carries the label the card layout shows.
 * The explicit roles keep the table semantics when the media query switches the
 * display type to cards.
 */
describe('ReleaseInventoryTable narrow-screen structure (W15)', () => {
  it('exposes a labelled, keyboard-scrollable region around the table', async () => {
    const wrapper = await mountTable([release()]);

    const region = wrapper.get('[role="region"]');
    expect(region.attributes('tabindex')).toBe('0');
    expect(region.attributes('aria-label')).toBe('发布清单表格（可横向滚动）');
    expect(region.get('table').attributes('role')).toBe('table');
  });

  it('labels every data cell and keeps row/column semantics explicit', async () => {
    const wrapper = await mountTable([release()]);

    expect(wrapper.get('thead').attributes('role')).toBe('rowgroup');
    expect(wrapper.get('tbody').attributes('role')).toBe('rowgroup');
    expect(wrapper.get('thead tr').attributes('role')).toBe('row');
    expect(wrapper.get('tbody tr').attributes('role')).toBe('row');
    // canEmergency/canConvergence are off in this mount, so the emergency column
    // (and only it) is absent; Release has no data-label because it is the card header.
    expect(wrapper.findAll('th[role="columnheader"]')).toHaveLength(6);
    expect(wrapper.findAll('td[role="cell"]')).toHaveLength(6);

    // TASK-394 (direction C): the grid is the inventory's, not the old
    // Release/status/Chart/Revision/digest/sync/actions one. Chart and the last sync time
    // moved into the row's context line, so they no longer carry a data-label.
    const labels = wrapper.findAll('td[data-label]').map((cell) => cell.attributes('data-label'));
    expect(labels).toEqual(['集群', '当前版本', '库存状态', '配置摘要', '操作']);
  });

  it('labels the emergency column when the capability renders it', () => {
    const wrapper = mount(ReleaseInventoryTable, {
      props: { releases: [release()], canEmergency: true, canCreateOperation: true },
      global: { stubs: { RouterLink: true } },
    });

    expect(wrapper.findAll('th[role="columnheader"]')).toHaveLength(7);
    const labels = wrapper.findAll('td[data-label]').map((cell) => cell.attributes('data-label'));
    expect(labels).toEqual(['集群', '当前版本', '库存状态', '紧急变更', '配置摘要', '操作']);
  });
});
