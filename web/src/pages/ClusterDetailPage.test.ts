import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import ClusterDetailPage from './ClusterDetailPage.vue';
import { disableCluster, getCluster } from '@/connect/cluster-api';
import type * as ClusterApi from '@/connect/cluster-api';
import { useAuthStore } from '@/stores/auth';

vi.mock('@/connect/cluster-api', async (importOriginal) => {
  const original = await importOriginal<typeof ClusterApi>();
  return { ...original, getCluster: vi.fn(), disableCluster: vi.fn() };
});

function dialogPanel(): HTMLElement {
  const panel = document.querySelector<HTMLElement>('.app-dialog__panel');
  if (!panel) throw new Error('disable dialog not rendered');
  return panel;
}

function clickButton(root: HTMLElement, label: string): void {
  const button = Array.from(root.querySelectorAll('button')).find((element) => element.textContent?.trim() === label);
  if (!button) throw new Error(`button not found: ${label}`);
  button.click();
}

beforeEach(() => {
  setActivePinia(createPinia());
  vi.unstubAllEnvs();
  vi.mocked(getCluster).mockReset().mockResolvedValue({
    id: 'cluster-1',
    name: 'staging',
    customerId: 'customer-1',
    enabled: true,
    version: 1,
    routeCount: 0,
    imageRules: [],
    chartRules: [],
  });
  vi.mocked(disableCluster).mockReset().mockResolvedValue(undefined);
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: {
      $typeName: 'auth.v1.SessionUser',
      id: 'viewer-1',
      username: 'viewer',
      roles: ['viewer'],
      activeOrgId: 'org-1',
    },
  });
});

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters', name: 'ClusterList', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters/:clusterId', name: 'ClusterDetail', component: ClusterDetailPage },
      { path: '/customers/:customerId/clusters/:clusterId/operators', name: 'OperatorList', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters/:clusterId/edit', name: 'ClusterEdit', component: { template: '<div />' } },
      // The page links into the release subtree (W3); an unregistered target
      // makes RouterLink throw and takes the whole header down.
      { path: '/customers/:customerId/clusters/:clusterId/releases', name: 'ReleaseInventory', component: { template: '<div />' } },
    ],
  });
  await router.push('/customers/customer-1/clusters/cluster-1');
  await router.isReady();
  const wrapper = mount(ClusterDetailPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

describe('ClusterDetailPage operator navigation', () => {
  it('shows the read-only Operators entry to a viewer when the feature is enabled', async () => {
    const wrapper = await mountPage();

    const operatorsLink = wrapper.findAll('a').find((link) => link.text() === 'Operator 列表');
    expect(operatorsLink?.attributes('href')).toBe('/customers/customer-1/clusters/cluster-1/operators');
    expect(wrapper.text()).not.toContain('编辑');
  });

  it('removes the Operators entry when the feature is disabled', async () => {
    vi.stubEnv('VITE_FEATURE_OPERATOR_MANAGEMENT', 'false');

    const wrapper = await mountPage();

    expect(wrapper.findAll('a').some((link) => link.text() === 'Operators')).toBe(false);
  });
});

describe('ClusterDetailPage disable confirmation (TASK-281)', () => {
  it('never disables on cancel and calls the RPC on confirm', async () => {
    useAuthStore().$patch({
      user: {
        $typeName: 'auth.v1.SessionUser',
        id: 'admin-1',
        username: 'admin',
        roles: ['release_admin'],
        activeOrgId: 'org-1',
      },
    });
    const wrapper = await mountPage();

    // Cancel: the shared dialog closes and neither original path runs.
    await wrapper.findAll('button').find((button) => button.text() === '停用集群')?.trigger('click');
    let panel = dialogPanel();
    expect(panel.getAttribute('role')).toBe('alertdialog');
    expect(document.getElementById(panel.getAttribute('aria-labelledby')!)?.textContent).toBe('停用集群');
    clickButton(panel, '取消');
    await flushPromises();
    expect(disableCluster).not.toHaveBeenCalled();
    expect(document.querySelector('.app-dialog__panel')).toBeNull();

    // A backdrop click is an accidental dismissal for a destructive flow, not a
    // confirmation: the dialog stays open and nothing runs (closeOnBackdrop=false).
    await wrapper.findAll('button').find((button) => button.text() === '停用集群')?.trigger('click');
    panel = dialogPanel();
    document.querySelector<HTMLElement>('.app-dialog')!.click();
    await flushPromises();
    expect(document.querySelector('.app-dialog__panel')).not.toBeNull();
    expect(disableCluster).not.toHaveBeenCalled();
    clickButton(panel, '取消');
    await flushPromises();
    expect(document.querySelector('.app-dialog__panel')).toBeNull();

    // Confirm: the same path `window.confirm` used to gate runs unchanged.
    await wrapper.findAll('button').find((button) => button.text() === '停用集群')?.trigger('click');
    panel = dialogPanel();
    clickButton(panel, '确认停用');
    await flushPromises();
    expect(disableCluster).toHaveBeenCalledWith('cluster-1');

    wrapper.unmount();
  });
});
