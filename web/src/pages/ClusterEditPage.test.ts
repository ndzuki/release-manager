import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import { nextTick } from 'vue';
import ClusterEditPage from './ClusterEditPage.vue';
import { getCluster } from '@/connect/cluster-api';
import type * as ClusterApi from '@/connect/cluster-api';
import { useAuthStore } from '@/stores/auth';
import { useClusterStore } from '@/stores/cluster';

/*
 * TASK-271 (A11y subset ②) — the sixth gap. The page's own `role="alert"` block only
 * carries `saveError.message`; the per-field violation had been rendered inside the
 * cluster name's wrapping <label>, so it was part of the control's accessible NAME.
 * The page had no test file at all before this, so the orphaned violation was also
 * invisible to the suite.
 */

vi.mock('@/connect/cluster-api', async (importOriginal) => {
  const original = await importOriginal<typeof ClusterApi>();
  return { ...original, getCluster: vi.fn() };
});

beforeEach(() => {
  setActivePinia(createPinia());
  vi.mocked(getCluster).mockReset();
  vi.mocked(getCluster).mockResolvedValue({
    id: 'cluster-1',
    name: 'staging',
    customerId: 'customer-1',
    enabled: true,
    version: 3,
    routeCount: 0,
    imageRules: [],
    chartRules: [],
  });
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
});

async function mountPage(path: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters/new', name: 'ClusterNew', component: ClusterEditPage },
      { path: '/customers/:customerId/clusters/:clusterId/edit', name: 'ClusterEdit', component: ClusterEditPage },
      { path: '/customers/:customerId/clusters', name: 'ClusterList', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters/:clusterId', name: 'ClusterDetail', component: { template: '<div />' } },
    ],
  });
  await router.push(path);
  await router.isReady();
  const wrapper = mount(ClusterEditPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

/** Mounts the create route, whose page has no clusterId: `startCreate()` runs instead
 * of a load, so no API call is issued. */
function mountCreatePage() {
  return mountPage('/customers/customer-1/clusters/new?customerName=Acme');
}

describe('ClusterEditPage field errors are described, not named', () => {
  it('describes the cluster name control with its violation', async () => {
    const wrapper = await mountCreatePage();
    const store = useClusterStore();
    store.saveError = {
      code: 'client_validation',
      message: '请修正表单中的错误',
      fieldViolations: [{ field: 'name', description: '名称不能为空' }],
    };
    await nextTick();

    const input = wrapper.get('.cluster-fields input[maxlength="254"]');
    const field = input.element.parentElement as HTMLElement;
    const describedBy = input.attributes('aria-describedby');
    const label = field.querySelector('label')!;

    // ① the accessible name is the label text only — the error is not in it ...
    expect(label.getAttribute('for')).toBe(input.attributes('id'));
    expect(label.textContent).not.toContain('名称不能为空');
    expect(label.querySelector('.form-field__error')).toBeNull();
    // ② ... it is reachable as the control's description instead. The page-level alert
    // is the first role="alert" in the document, so this is scoped to the field.
    expect(describedBy).toBeTruthy();
    const description = field.querySelector(`#${describedBy}`)!;
    expect(description.textContent).toContain('名称不能为空');
    expect(description.getAttribute('role')).toBe('alert');
    expect(input.attributes('aria-invalid')).toBe('true');
    // The page-level alert is unchanged and still carries only the summary message.
    const alert = wrapper.get('.save-error[role="alert"]');
    expect(alert.text()).toContain('请修正表单中的错误');
  });

  it('leaves the name control valid and undescribed when there is no violation', async () => {
    const wrapper = await mountCreatePage();

    const input = wrapper.get('.cluster-fields .form-field input[maxlength="254"]');

    expect(input.attributes('aria-invalid')).toBeUndefined();
    expect(input.attributes('aria-describedby')).toBeUndefined();
    expect(wrapper.find('.form-field__error').exists()).toBe(false);
  });
});

/*
 * TASK-394 (direction C). The hand-rolled `.eyebrow` + `<h1>` pair is PageHeader now, and
 * the category line must survive the migration verbatim: "新建集群" while creating,
 * "编辑集群" while editing. The crumb trail names the customer and the cluster; neither
 * may degrade to its route identifier.
 */
describe('ClusterEditPage direction C page header', () => {
  it('keeps the create category line and titles the page with the same label', async () => {
    const wrapper = await mountCreatePage();

    expect(wrapper.get('.page-header__eyebrow').text()).toBe('新建集群');
    expect(wrapper.get('.page-header__title').text()).toBe('新建集群');
  });

  it('names the customer in the create breadcrumb', async () => {
    const wrapper = await mountCreatePage();

    const crumbs = wrapper.get('.page-header__crumbs').text();
    expect(crumbs).toContain('Acme');
    expect(crumbs).not.toContain('customer-1');
  });

  it('keeps the edit category line and shows the cluster and customer names', async () => {
    const wrapper = await mountPage('/customers/customer-1/clusters/cluster-1/edit?customerName=Acme');

    expect(wrapper.get('.page-header__eyebrow').text()).toBe('编辑集群');
    expect(wrapper.get('.page-header__title').text()).toBe('staging');
    const crumbs = wrapper.get('.page-header__crumbs').text();
    expect(crumbs).toContain('Acme');
    expect(crumbs).toContain('staging');
    expect(crumbs).not.toContain('cluster-1');
  });
});
