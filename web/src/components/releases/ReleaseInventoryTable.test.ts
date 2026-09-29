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
