import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import type { DefinitionCreateForm } from '@/composables/useDefinitionCreate';
import type { ClusterSummary } from '@/types/cluster';
import type { CustomerSummary } from '@/types/customer';
import DefinitionCreateDialog from './DefinitionCreateDialog.vue';

/*
 * TASK-363: the create dialog was carved out of DefinitionsPage. The page owns the
 * form; this component owns the markup, the per-instance label ids and the intent
 * events. The two failure slots must render INSIDE the dialog: the page-level
 * <ErrorState> is behind the fixed backdrop.
 *
 * TASK-394 follow-up: customer and cluster are picked from lists instead of typed as
 * opaque ids. The dialog stays presentational — the page passes the lists and reloads
 * the cluster list when the customer changes — so these tests drive props/emits only.
 */

const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

function blankForm(): DefinitionCreateForm {
  return { customerId: '', clusterId: '', namespace: '', releaseName: '', chartName: '', enabled: true };
}

const customers: CustomerSummary[] = [
  { id: '11111111-1111-4111-8111-111111111111', name: 'Acme', slug: 'acme', status: 'active', version: 1 },
  { id: '22222222-2222-4222-8222-222222222222', name: 'Globex', slug: 'globex', status: 'active', version: 1 },
];
const clusters: ClusterSummary[] = [
  {
    id: 'prod-west',
    name: 'Prod West',
    customerId: '11111111-1111-4111-8111-111111111111',
    enabled: true,
    version: 1,
    routeCount: 0,
  },
];

function mountCreate(
  overrides: {
    form?: DefinitionCreateForm;
    saving?: boolean;
    validationError?: string;
    failure?: string | null;
    clusters?: ClusterSummary[];
    loadingCustomers?: boolean;
    loadingClusters?: boolean;
    customersError?: string | null;
    clustersError?: string | null;
  } = {},
) {
  const wrapper = mount(DefinitionCreateDialog, {
    props: {
      open: true,
      saving: overrides.saving ?? false,
      validationError: overrides.validationError ?? '',
      failure: overrides.failure ?? null,
      form: overrides.form ?? blankForm(),
      customers,
      clusters: overrides.clusters ?? clusters,
      loadingCustomers: overrides.loadingCustomers ?? false,
      loadingClusters: overrides.loadingClusters ?? false,
      customersError: overrides.customersError ?? null,
      clustersError: overrides.clustersError ?? null,
    },
  });
  mounted.push(wrapper);
  return wrapper;
}

function button(root: ParentNode, label: string): HTMLButtonElement {
  const element = Array.from(root.querySelectorAll<HTMLButtonElement>('button')).find(
    (candidate) => candidate.textContent?.trim() === label,
  );
  if (!element) throw new Error(`button not found: ${label}`);
  return element;
}

describe('DefinitionCreateDialog', () => {
  it('renders the five fields with their own labels and the enabled checkbox', () => {
    mountCreate();

    const controls = Array.from(
      document.querySelectorAll<HTMLElement>('.definitions__field input, .definitions__field select'),
    );
    expect(controls).toHaveLength(5);
    for (const control of controls) {
      const id = control.getAttribute('id');
      expect(id).toBeTruthy();
      const label = document.querySelector(`.definitions__field label[for="${id}"]`);
      expect(label?.textContent?.trim()).not.toBe('');
    }
    expect(new Set(controls.map((control) => control.getAttribute('id'))).size).toBe(5);
    expect(document.querySelector<HTMLInputElement>('input[name="create-enabled"]')?.checked).toBe(true);
  });

  it('offers customers and clusters as lists and clears the cluster when the customer changes', async () => {
    const form = blankForm();
    const wrapper = mountCreate({ form });

    const customer = document.querySelector<HTMLSelectElement>('select[name="create-customerId"]')!;
    const cluster = document.querySelector<HTMLSelectElement>('select[name="create-clusterId"]')!;
    // A cluster id only means something inside one customer.
    expect(cluster.disabled).toBe(true);

    customer.value = '11111111-1111-4111-8111-111111111111';
    customer.dispatchEvent(new Event('change'));
    await nextTick();
    expect(form.customerId).toBe('11111111-1111-4111-8111-111111111111');
    expect(wrapper.emitted('customerChange')).toHaveLength(1);
    expect(customer.options[customer.selectedIndex]?.textContent?.trim()).toBe('Acme · 标识 acme');
    // The picked id is shown verbatim so it can still be copied/recognised.
    expect(document.body.textContent).toContain('11111111-1111-4111-8111-111111111111');

    cluster.value = 'prod-west';
    cluster.dispatchEvent(new Event('change'));
    await nextTick();
    expect(form.clusterId).toBe('prod-west');
    expect(cluster.options[cluster.selectedIndex]?.textContent?.trim()).toBe('Prod West · 集群 prod-west');

    customer.value = '22222222-2222-4222-8222-222222222222';
    customer.dispatchEvent(new Event('change'));
    await nextTick();
    expect(form.clusterId).toBe('');
    expect(wrapper.emitted('customerChange')).toHaveLength(2);
  });

  it('disables the cluster picker and explains why until a customer is chosen', () => {
    mountCreate({ clusters: [] });

    const cluster = document.querySelector<HTMLSelectElement>('select[name="create-clusterId"]')!;
    expect(cluster.disabled).toBe(true);
    expect(cluster.options[0]?.textContent?.trim()).toBe('先选客户');
  });

  it('edits the form it was handed in place (the page owns it)', async () => {
    const form = blankForm();
    mountCreate({ form });

    const namespace = document.querySelector<HTMLInputElement>('input[name="create-namespace"]')!;
    namespace.value = 'apps';
    namespace.dispatchEvent(new Event('input'));
    await nextTick();
    document.querySelector<HTMLInputElement>('input[name="create-enabled"]')!.click();
    await nextTick();

    expect(form.namespace).toBe('apps');
    expect(form.enabled).toBe(false);
  });

  it('renders both failure slots as alerts inside the dialog', () => {
    document.body.innerHTML = '';
    const wrapper = mount(DefinitionCreateDialog, {
      props: {
        open: true,
        saving: false,
        validationError: '请选择客户、集群并填写 Release 名称',
        failure: '相同来源与目标的定义已存在',
        form: blankForm(),
        customers,
        clusters,
        loadingCustomers: false,
        loadingClusters: false,
        customersError: null,
        clustersError: null,
      },
    });
    mounted.push(wrapper);

    const validation = document.querySelector<HTMLElement>('[data-testid="definitions-create-validation"]');
    const failure = document.querySelector<HTMLElement>('[data-testid="definitions-create-error"]');
    expect(validation?.textContent?.trim()).toBe('请选择客户、集群并填写 Release 名称');
    expect(validation?.getAttribute('role')).toBe('alert');
    expect(failure?.textContent?.trim()).toBe('相同来源与目标的定义已存在');
    expect(failure?.getAttribute('role')).toBe('alert');
    expect(document.querySelector('.app-dialog__body')?.contains(failure ?? null)).toBe(true);
  });

  it('surfaces a failed list load without hiding the form', () => {
    mountCreate({ customersError: '客户列表读取失败', clusters: [] });

    const alert = document.querySelector<HTMLElement>('.definitions__create [role="alert"]');
    expect(alert?.textContent?.trim()).toBe('客户列表读取失败');
    expect(document.querySelector('input[name="create-releaseName"]')).not.toBeNull();
  });

  it('reports submit and close as events', async () => {
    const wrapper = mountCreate();

    document.querySelector<HTMLFormElement>('.definitions__create form')!.dispatchEvent(new Event('submit'));
    await nextTick();
    expect(wrapper.emitted('submit')).toHaveLength(1);

    button(document, '取消').click();
    await nextTick();
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('disables both actions while a create is in flight', () => {
    mountCreate({ saving: true });

    expect(button(document, '取消').disabled).toBe(true);
    expect(document.querySelector<HTMLButtonElement>('[data-testid="definitions-create-submit"]')?.disabled).toBe(true);
  });
});
