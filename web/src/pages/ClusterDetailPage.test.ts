import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import ClusterDetailPage from './ClusterDetailPage.vue';
import { disableCluster, getCluster } from '@/connect/cluster-api';
import type * as ClusterApi from '@/connect/cluster-api';
import { getCustomer } from '@/connect/customer-api';
import type * as CustomerApi from '@/connect/customer-api';
import { useAuthStore } from '@/stores/auth';

vi.mock('@/connect/cluster-api', async (importOriginal) => {
  const original = await importOriginal<typeof ClusterApi>();
  return { ...original, getCluster: vi.fn(), disableCluster: vi.fn() };
});

/* Direction C (TASK-394): the customer level of the breadcrumb is resolved by
 * `useScopeNames`, which asks the API when the query does not carry the name. */
vi.mock('@/connect/customer-api', async (importOriginal) => {
  const original = await importOriginal<typeof CustomerApi>();
  return { ...original, getCustomer: vi.fn() };
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
  vi.mocked(getCustomer).mockReset().mockResolvedValue({
    id: 'customer-1',
    name: 'Acme',
    slug: 'acme',
    status: 'active',
    version: 1,
  } as never);
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

/*
 * W10 (UX-041): the destructive path ended silently — the dialog closed and the
 * badge changed, which is exactly the feedback an operator cannot rely on after an
 * irreversible action. The refusal was worse: `store.disable` rethrows and nothing
 * caught it, so a refused disable produced an unhandled rejection and no on-screen
 * reason. Both outcomes now render through the shared Toast primitive.
 */
describe('ClusterDetailPage disable feedback (W10)', () => {
  async function confirmDisableAsAdmin(): Promise<ReturnType<typeof mountPage>> {
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
    await wrapper.findAll('button').find((button) => button.text() === '停用集群')?.trigger('click');
    clickButton(dialogPanel(), '确认停用');
    await flushPromises();
    return wrapper;
  }

  it('announces a successful disable with a polite success toast', async () => {
    const wrapper = await confirmDisableAsAdmin();

    const notice = wrapper.get('[data-testid="cluster-detail-notice"]');
    expect(notice.attributes('role')).toBe('status');
    expect(notice.text()).toContain('已停用集群 staging');
    wrapper.unmount();
  });

  it('shows the refusal instead of leaving an unhandled rejection', async () => {
    vi.mocked(disableCluster).mockRejectedValue(new Error('cluster disable refused'));
    const wrapper = await confirmDisableAsAdmin();

    const notice = wrapper.get('[data-testid="cluster-detail-notice"]');
    // Assertive: a refused destructive action must not be announced politely.
    expect(notice.attributes('role')).toBe('alert');
    expect(notice.text()).toContain('cluster disable refused');
    wrapper.unmount();
  });

  it('dismisses the notice without touching the dialog state', async () => {
    const wrapper = await confirmDisableAsAdmin();
    expect(wrapper.find('[data-testid="cluster-detail-notice"]').exists()).toBe(true);

    await wrapper.get('[data-testid="cluster-detail-notice-dismiss"]').trigger('click');

    expect(wrapper.find('[data-testid="cluster-detail-notice"]').exists()).toBe(false);
    wrapper.unmount();
  });
});

/*
 * TASK-394 (direction C). The hand-rolled `.eyebrow` + `<h1>` pair is PageHeader now. The
 * category line ("集群") survives verbatim, the subtitle keeps naming the release-target
 * state, and the trail carries the customer/cluster NAMES — a level that cannot be
 * resolved is dropped instead of degrading to the route identifier.
 */
describe('ClusterDetailPage direction C page header', () => {
  it('keeps the 集群 eyebrow, the cluster title and the target-state subtitle', async () => {
    const wrapper = await mountPage();

    expect(wrapper.get('.page-header__eyebrow').text()).toBe('集群');
    expect(wrapper.get('.page-header__title').text()).toBe('staging');
    expect(wrapper.get('.page-header__subtitle').text()).toBe('生效中的发布目标');
  });

  it('names the customer and the cluster in the breadcrumb', async () => {
    const wrapper = await mountPage();
    await flushPromises();

    const crumbs = wrapper.get('.page-header__crumbs').text();
    expect(crumbs).toContain('Acme');
    expect(crumbs).toContain('staging');
    expect(crumbs).not.toContain('customer-1');
    expect(crumbs).not.toContain('cluster-1');
  });

  it('drops the customer level instead of printing its identifier', async () => {
    vi.mocked(getCustomer).mockRejectedValue(new Error('not found'));

    const wrapper = await mountPage();
    await flushPromises();

    const crumbs = wrapper.get('.page-header__crumbs').text();
    expect(crumbs).toContain('staging');
    expect(crumbs).not.toContain('customer-1');
  });
});

/*
 * W21 (UX-032). The three-column summary had no breakpoint, so at a narrow width the
 * three tracks were squeezed to their min-content and the page scrolled sideways.
 * happy-dom does not evaluate scoped CSS (no layout, no media queries), so this reads
 * the SFC's stylesheet: the console's shared 48rem breakpoint must collapse the grid
 * to one column.
 */
describe('ClusterDetailPage narrow summary (W21)', () => {
  it('collapses the three-column summary to one column below 48rem', () => {
    const source = readFileSync(resolve(__dirname, 'ClusterDetailPage.vue'), 'utf8');
    const style = source.replace(/\/\*[\s\S]*?\*\//g, '');

    expect(style).toMatch(/\.summary\s*\{[^}]*grid-template-columns:\s*repeat\(3,\s*1fr\)/);
    const narrow = style.match(/@media \(max-width: 48rem\)\s*\{([\s\S]*?)\n\}/);
    expect(narrow, 'no 48rem breakpoint in ClusterDetailPage.vue').not.toBeNull();
    expect(narrow![1]).toMatch(/\.summary\s*\{\s*grid-template-columns:\s*1fr/);
  });
});
