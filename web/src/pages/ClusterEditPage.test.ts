import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import { nextTick } from 'vue';
import ClusterEditPage from './ClusterEditPage.vue';
import { useAuthStore } from '@/stores/auth';
import { useClusterStore } from '@/stores/cluster';

/*
 * TASK-271 (A11y subset ②) — the sixth gap. The page's own `role="alert"` block only
 * carries `saveError.message`; the per-field violation had been rendered inside the
 * cluster name's wrapping <label>, so it was part of the control's accessible NAME.
 * The page had no test file at all before this, so the orphaned violation was also
 * invisible to the suite.
 */

beforeEach(() => {
  setActivePinia(createPinia());
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

/** Mounts the create route, whose page has no clusterId: `startCreate()` runs instead
 * of a load, so no API call is issued. */
async function mountCreatePage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters/new', name: 'ClusterNew', component: ClusterEditPage },
      { path: '/customers/:customerId/clusters', name: 'ClusterList', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters/:clusterId', name: 'ClusterDetail', component: { template: '<div />' } },
    ],
  });
  await router.push('/customers/customer-1/clusters/new');
  await router.isReady();
  const wrapper = mount(ClusterEditPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
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

    const field = wrapper.get('.cluster-fields .form-field');
    const input = field.get('input[maxlength="254"]');
    const describedBy = input.attributes('aria-describedby');
    const label = field.get('label');

    // ① the accessible name is the label text only — the error is not in it ...
    expect(label.attributes('for')).toBe(input.attributes('id'));
    expect(label.text()).not.toContain('名称不能为空');
    expect(label.find('.form-field__error').exists()).toBe(false);
    // ② ... it is reachable as the control's description instead. The page-level alert
    // is the first role="alert" in the document, so this is scoped to the field.
    expect(describedBy).toBeTruthy();
    expect(field.get(`#${describedBy}`).text()).toContain('名称不能为空');
    expect(field.get(`#${describedBy}`).attributes('role')).toBe('alert');
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
